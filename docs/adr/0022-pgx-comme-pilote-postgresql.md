# ADR-0022 — pgx en accès natif, `database/sql` réservé aux migrations

**Date** : 2026-08-06
**Statut** : Accepted

## Contexte

L'ADR-0008 fait de PostgreSQL le journal d'audit **et** le transport des
events. Deux conséquences fixent le cahier des charges du pilote, et aucune des
deux n'est générique :

- **`LISTEN` / `NOTIFY` est le chemin de lecture du direct.** Ce n'est pas une
  requête : c'est une connexion qui reste ouverte et reçoit des messages
  asynchrones hors de tout cycle requête/réponse.
- **PostgreSQL entre dans le chemin chaud du streaming.** Le nombre de
  connexions et le pooling deviennent des paramètres de *latence*, plus
  seulement de capacité — l'ADR-0008 le dit explicitement.

S'ajoute une contrainte de sens contraire : l'ADR-0012 a retenu **goose**, qui
parle `database/sql` et rien d'autre.

## Solutions envisagées

### Option A — `database/sql` + lib/pq
Le pilote historique, le plus documenté.
- **Rejetée parce que** : lib/pq est en mode maintenance et son auteur oriente
  vers pgx depuis des années. Surtout, `pq.Listener` vit entièrement à côté de
  `database/sql` — la seule fonctionnalité dont on a besoin est déjà hors du
  cadre que l'option prétend apporter.

### Option B — `database/sql` + pgx en pilote `stdlib`
On garde l'interface standard, donc la portabilité théorique vers un autre
SGBD, avec pgx dessous.
- **Avantage** : une seule façon de parler à la base dans tout le produit.
- **Rejetée parce que** : la portabilité qu'on paierait est fictive. Le schéma
  utilise le partitionnement déclaratif, un trigger `SECURITY DEFINER` à table
  de transition, `ON CONFLICT`, du JSONB et des `GRANT` par colonne — il n'y a
  pas d'autre SGBD au bout de cette abstraction. Et le prix est réel :
  `database/sql` est un pool qui rend les connexions, alors que `LISTEN` exige
  de garder LA connexion ; en la rendant au pool, on perd les notifications
  sans erreur visible.

### Option C — pgx en accès natif ✅ (retenue)
`pgx/v5` et `pgxpool` pour tout le chemin applicatif ; le pilote `stdlib`
uniquement là où goose l'impose.
- **Choisie parce que** : `pgxpool` expose le dimensionnement que l'ADR-0008
  désigne comme un paramètre de latence ; `Conn.WaitForNotification` traite
  `LISTEN` pour ce qu'il est, une connexion dédiée et non une requête ; et le
  protocole binaire évite un aller-retour de texte sur des payloads JSONB qui
  sont l'essentiel du volume.

## Décision

**`github.com/jackc/pgx/v5` + `pgxpool`** pour l'accès applicatif.

**`database/sql` uniquement dans `store.Migrate`**, où goose l'impose. C'est
aussi le bon endroit : une migration est un usage mono-connexion, et l'ouvrir à
part évite qu'un verrou de migration soit pris sur une connexion du pool
applicatif.

**Deux identités de connexion, deux constructeurs** (ADR-0008, ADR-0012) :

- `Open` rend un `*Store`, accès complet, pour `api`, `git` et `controller` ;
- `OpenIngest` rend un `*Ingest`, qui n'expose que l'ajout d'events.

La restriction **réelle** est le rôle PostgreSQL, restreint à `INSERT` sur les
tables d'events ; le type la rend visible dans le code. Les deux comptent, et
pour des raisons différentes : le type empêche d'écrire par distraction du code
qui ferait autre chose, le rôle empêche que ce code ait le moindre effet s'il
finissait par exister. Un test négatif vérifie le second — l'ADR-0008 l'exigeait,
et un `GRANT` non testé n'est qu'une intention.

**`application_name` est positionné par identité** (`warden-app`,
`warden-ingest`). Savoir *quelle* identité tient une connexion est la première
question posée le jour où le pool sature.

## Conséquences

- **Avantages** :
  - `LISTEN` / `NOTIFY` est traité par une API qui le reconnaît, au lieu d'être
    contourné à côté du pool ;
  - le dimensionnement du pool est un réglage explicite, à mesurer en phase 1
    comme l'ADR-0008 le demande ;
  - les types PostgreSQL riches — JSONB, `timestamptz`, tableaux — n'ont pas à
    transiter par des conversions en texte.

- **Contraintes** :
  - **Les types pgx n'ont pas le droit de sortir de `outbound/store`.** Le
    découpage de l'ADR-0019 le garantit par construction — `depguard` refuse
    `github.com/jackc` dans le cœur — mais c'est ce qui rend le choix
    réversible, et c'est donc la propriété à ne pas laisser filer.
  - **Une connexion `LISTEN` est hors du pool.** Une par réplica (ADR-0008), et
    il faudra la surveiller comme une ressource à part : elle ne se manifestera
    pas dans les métriques du pool.
  - **`LISTEN` ne survit pas à un pooler en mode transaction.** Si PgBouncer ou
    l'équivalent CloudNativePG s'intercale un jour, le direct s'arrête sans
    erreur — les events continueront d'être journalisés, seul le suivi temps
    réel cessera. C'est exactement le mode de panne silencieux qu'il faut
    détecter avant de l'introduire.

- **Dette technique éventuelle** :
  - Les valeurs du pool (`MaxConns = 10`, `MinConns = 2`) sont un point de
    départ, pas un réglage. La phase 1 est celle qui mesure le volume d'events
    (ADR-0008) ; c'est au même moment qu'elles doivent être établies.
  - `CopyFrom` de pgx serait plus rapide qu'un `INSERT` multi-lignes pour les
    gros lots, mais ne supporte pas `ON CONFLICT DO NOTHING` — donc pas
    l'idempotence, qui n'est pas négociable. À rouvrir seulement si la mesure
    montre que l'insertion est le goulot.

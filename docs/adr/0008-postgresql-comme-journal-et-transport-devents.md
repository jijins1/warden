# ADR-0008 — PostgreSQL comme journal et comme transport des events

**Date** : 2026-08-04
**Statut** : Accepted

## Contexte

`specs/initial.md` §9.2 prescrit un bus persistant entre le runner et le serveur
(`runner → bus → server → WS → CLI`), et §14 laisse ouvert le choix entre NATS
JetStream et Redis Streams. Le motif invoqué est juste : **jamais de connexion
directe pod ↔ client**, sinon le stream meurt avec le pod.

Mais le bus n'est pas la seule façon de satisfaire ce motif. Les exigences
réelles sont énumérées au §9.1 — survie à la déconnexion du client, detach /
reattach, replay depuis un offset, survie à un rolling update multi-réplica — et
le §6.3 en ajoute une plus forte : les events sont persistés **au fil de l'eau**,
jamais bufferisés jusqu'à la fin, parce que la question d'audit qui compte est
« qu'est-ce qui avait déjà été modifié » quand un run est tué brutalement.

Or c'est précisément cette exigence qu'un bus satisfait le moins bien. Avec un
bus, un event acquitté est durable **dans le bus**, mais pas encore dans le
journal ; il existe une fenêtre où le système a la donnée sans pouvoir la
requêter, et un consommateur en retard rouvre exactement le trou que §6.3 cherche
à fermer.

Le volume, enfin, ne plaide pas pour un bus : un run Ansible produit des
milliers d'events, pas des millions, à un rythme de l'ordre de la centaine par
seconde en pointe.

## Solutions envisagées

### Option A — NATS JetStream
Stream à stockage fichier, permissions par sujet — le runner ne peut publier que
sur `runs.<id>.events` et ne s'abonne à rien.
- **Avantage réel** : absorbe une indisponibilité de PostgreSQL pour les runs en
  vol.
- **Rejetée parce que** : c'est un second système avec état à exploiter,
  sauvegarder et mettre à jour, pour un bénéfice qui ne porte que sur ce cas. Et
  si PostgreSQL est indisponible, le control plane l'est aussi : plus aucun run
  ne démarre, plus aucun statut ne s'écrit. Un run dont l'audit ne peut pas
  s'écrire doit s'arrêter, pas continuer en aveugle — la disponibilité qu'on
  achèterait ainsi n'est pas celle qu'on veut.

### Option B — Redis Streams
Consumer groups et replay par offset.
- **Rejetée parce que** : rétention bornée par la mémoire, durabilité dépendante
  du réglage AOF — `everysec` laisse une fenêtre de perte — et ACL plus
  grossières sur les streams. Mal adapté à une donnée d'audit.

### Option C — PostgreSQL seul, avec un endpoint d'ingestion ✅ (retenue)
Le runner écrit ses events sur un endpoint d'ingestion dédié, qui les insère
directement dans le journal.
- **Choisie parce que** : elle supprime l'écart entre « durable » et
  « journalisé », qui est l'exigence du §6.3 ; elle supprime un système avec état
  de l'exploitation ; le replay n'est plus borné par une fenêtre de rétention
  mais par la rétention du journal lui-même ; et le statut du run et ses events
  deviennent inscriptibles dans la **même transaction**.

## Décision

PostgreSQL est à la fois le journal et le transport des events. Aucun bus.

**L'offset est assigné par le runner**, monotone par run. La conséquence est que
l'insertion est idempotente sur `(run_id, seq)` : les réémissions sont sans
danger, l'ingestion se parallélise sur N réplicas sans contrainte d'ordre, et
l'ordre est reconstitué à la lecture.

**Chemin d'écriture.** Le runner envoie ses events par lots courts — plafonnés en
nombre et en délai, de l'ordre de quelques centaines de millisecondes — vers un
endpoint d'ingestion. En cas d'échec il retente ; **au-delà d'un seuil, le run est
arrêté**. Un run dont l'audit ne s'écrit pas ne doit pas continuer.

**Isolation de l'ingestion.** L'endpoint est servi par le rôle `api`
(ADR-0006) mais sur un **port distinct**, avec sa propre règle d'ingress
NetworkPolicy et son **propre rôle PostgreSQL restreint à `INSERT`** sur la table
d'events. L'ADR-0007 dit que le runner ne joint jamais l'API : c'est à lire comme
« jamais le port de l'API ». Le code de l'ingestion est séparé de celui de l'API
dès le départ, pour que sa promotion en quatrième rôle déployé à part reste un
changement de déploiement et non de code.

**Chemin de lecture (WS).** À la connexion, le rôle `api` sert l'historique depuis
la table à partir de l'offset acquitté par le client, puis suit le direct par
`LISTEN` / `NOTIFY` — la notification ne portant que `run_id` et `seq`, jamais
l'event. `NOTIFY` ne délivre rien aux réplicas déconnectés : ce n'est pas un
problème, le rattrapage se fait par `seq` au même titre qu'une reconnexion
client. Le detach / reattach, le rolling update et le multi-réplica sont couverts
par le même mécanisme.

**Schéma, dès la première version :**

- Table d'events **partitionnée par date**, clé `(run_id, seq)`.
- **Index `(hôte, date)`** — la vue par hôte du §6.5 est la question la plus
  fréquente en incident, et un index ajouté après coup sur une table d'events est
  une opération pénible.
- Compteur `changed` **dénormalisé** sur la ligne de run : c'est la métrique
  d'audit principale (§6.5), elle ne doit pas coûter une agrégation.
- Les payloads `--diff` vont dans une **table séparée, avec un contrôle d'accès
  distinct**. Ils contiennent les secrets rendus (§6.5) ; les mélanger aux events
  ordinaires rendrait impossible d'ouvrir le journal sans ouvrir les secrets.

## Conséquences

- **Avantages** :
  - un seul système avec état à exploiter et à sauvegarder ;
  - durable ≡ journalisé, sans consommateur intermédiaire à surveiller ;
  - replay non borné par une fenêtre de rétention ;
  - statut et events dans la même transaction ;
  - le runner ne connaît qu'un endpoint HTTP, pas un client de bus.

- **Contraintes** :
  - **PostgreSQL entre dans le chemin chaud du streaming.** Son dimensionnement,
    le nombre de connexions et le pooling deviennent des paramètres de latence,
    plus seulement de capacité.
  - Écart assumé à la lettre du §9.2, à reporter dans la spec. Le motif du §9.2
    — jamais de connexion directe pod ↔ client — reste respecté.
  - L'isolation de l'ingestion repose sur une séparation par port et par rôle
    PostgreSQL, non par pod. Elle doit être couverte par un test négatif : un
    jeton de run ne doit rien pouvoir faire sur le port de l'API.

- **Dette technique éventuelle** :
  - Le volume réel d'events par run n'est pas mesuré. Il doit l'être en phase 1,
    qui est justement la phase de prototypage du streaming (§12).
  - Si ce volume dépasse ce que PostgreSQL absorbe confortablement, deux replis
    existent et ne s'excluent pas : insérer un bus **derrière la même interface
    d'ingestion**, ou déporter la table d'events vers un store dédié. La
    partition par date est là pour rendre le second possible ; l'interface
    d'ingestion pour rendre le premier possible sans toucher au runner.
  - `LISTEN` / `NOTIFY` ne passe pas à l'échelle sur un très grand nombre de
    canaux. Un canal unique filtré par réplica suffit à la cible visée, mais
    c'est un point à revoir avant toute montée en charge notable.

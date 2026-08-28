# ADR-0035 — Surface navigateur : une origine déclarée, un cookie, et UNE seule allowlist

**Date** : 2026-08-27
**Statut** : Accepted
**Complète** : [ADR-0032](0032-comptes-locaux-et-sessions.md), [ADR-0021](0021-modele-devent-normalise.md)

## Contexte

Le produit n'avait qu'un client : la CLI. Elle s'authentifie par
`Authorization: Bearer`, et rien dans l'API ne connaissait le navigateur. Ouvrir
une interface web bute sur trois faits, et aucun n'est cosmétique.

**Aucun CORS.** Pas une ligne, ni dans `internal/`, ni dans le chart. Une SPA
servie depuis une autre origine ne peut appeler aucun endpoint.

**Le WebSocket n'accepte pas d'en-tête.** L'API `WebSocket` du navigateur ne
permet pas de poser `Authorization`. Le flux `GET /runs/{run_ref}/stream` — le
cœur du produit — était donc inatteignable depuis une page, quel que soit le
travail fait sur le REST.

**`CheckOrigin` renvoyait `true` en dur.** Inoffensif tant que l'authentification
est un en-tête que seul un client délibéré pose. Le jour où une créance
*ambiante* existe, cette ligne devient un cross-site WebSocket hijacking : le
handshake WebSocket n'est pas soumis au CORS, et le navigateur y attache les
cookies tout seul.

## Décisions

### 1. La SPA est servie depuis une origine distincte

Retenu sur demande de l'exploitant. Le coût est réel et il est assumé : il faut
écrire un CORS avec credentials, durcir `CheckOrigin`, et protéger le CSRF —
trois choses que l'hébergement en même origine n'aurait pas demandées.

**Conséquence non négociable** : l'hôte de l'UI et celui de l'API doivent
partager le même **domaine enregistrable** (`warden-ui.example.com` et
`warden.example.com` → `example.com`). Same-site mais cross-origin : le cookie
`SameSite=Strict` part quand même. Sur deux domaines enregistrables distincts il
faudrait `SameSite=None`, et la défense CSRF reposerait entièrement sur le CORS.
Cette marche n'est pas descendue ici ; elle exigerait un amendement à ce
document.

### 2. UNE allowlist, consommée par le CORS ET par le WebSocket

`WARDEN_UI_ORIGINS`, liste séparée par des virgules, lue une fois et passée aux
deux consommateurs (`Server.origines`).

C'est **la** décision de ce document. Deux listes, ou une liste et une constante,
rendraient possible le seul scénario qui compte : durcir le CORS, oublier le
WebSocket, et croire la surface fermée alors que le flux d'audit reste ouvert à
n'importe quel site. Les faire dériver d'une même valeur rend l'oubli impossible
plutôt qu'improbable.

**Vide vaut « aucun navigateur »**, jamais « tous ». Un déploiement qui ne se
prononce pas ne doit pas s'ouvrir par accident, et l'ancien `CheckOrigin: true`
est précisément ce défaut-là.

`upgrader` cesse d'être une variable de paquet et devient un champ du serveur :
une globale ne peut pas consulter l'allowlist de *ce* déploiement.

### 3. Le cookie est un TRANSPORT, pas une seconde notion de session

`POST /auth/login` pose `warden_session` — `HttpOnly`, `Secure`,
`SameSite=Strict`, `Path=/` — **en plus** du corps JSON, jamais à la place. La
CLI lit toujours `token` et ne voit rien de ce changement.

Le cookie porte le **même jeton opaque** que le porteur. Sa résolution reste une
requête SQL sur `session` : aucun état en mémoire, donc aucune affinité de
réplica, et une révocation qui mord immédiatement sur tous les réplicas. C'est ce
qu'un JWT n'aurait pas donné, en plus d'imposer une clé de signature à
distribuer.

`Path=/` et non plus étroit : l'API sert `/runs`, `/auth/*` et `/admin/*`, et un
`Path=/api` se désaligne dès qu'un ingress réécrit le préfixe.

Le `logout` **expire le cookie** en plus de révoquer la session. Révoquer sans
effacer laisserait le navigateur renvoyer un jeton mort à chaque requête, donc
des 401 sans cause visible.

### 4. L'ordre du middleware : porteur, puis cookie, puis en-tête de dev

Le porteur reste **premier**, et un porteur invalide ne retombe pas sur le
cookie. Sans cela, la CLI changerait de comportement selon ce qu'un navigateur a
laissé dans le même magasin, et une session expirée continuerait de fonctionner
sans que personne le voie.

### 5. L'écriture par cookie exige `X-Warden-UI`

Sur toute méthode mutante authentifiée **par cookie**. La valeur n'est jamais
lue : seule la présence compte. Un formulaire ou une image d'un site tiers ne
peut pas poser un en-tête arbitraire sans déclencher une prévol, et la prévol
échoue sur une origine non déclarée.

C'est le second verrou **derrière** `SameSite=Strict`, pas son remplaçant :
Strict suppose le domaine enregistrable partagé, et un sous-domaine compromis
reste « same-site ». L'en-tête ne dépend pas de cette hypothèse.

En lecture il n'est pas exigé — un `GET` ne change rien, et l'imposer casserait
la navigation sans rien protéger.

### 6. `GET /runs/{run_ref}` est ajouté

Il n'existait pas. Son absence cassait la fonction première d'une interface web :
un lien profond `/runs/01J…` rechargé n'avait aucun moyen de retrouver son run,
la liste étant paginée par curseur.

Il passe par `requireRunAccess`, donc un run qu'on n'a pas le droit de voir
répond **404 et non 403** : distinguer les deux ferait de l'endpoint un oracle
des références qui existent, ce que l'ADR-0021 avait retiré en exposant des ULID.
Son type de fil est explicite et ne republie ni `ID` ni `UserID`.

### 7. `TerminationCause` entre dans `RunListItem`

La liste ne la portait pas. Une interface ne pouvait donc afficher que
« interrompu » tout seul — ce que le domaine qualifie lui-même de « presque
inutile » (`core/run.go`), un Ctrl-C étant un signal fort là où un
`client_disconnect_timeout` n'en est pas un.

La colonne est `NULL` sur tout run non interrompu ; un `COALESCE` la rend vide,
ce qui est exactement la sémantique que `Closure.Validate` impose déjà.

## Options rejetées

**Servir la SPA depuis le rôle `api` (`//go:embed`).** Même origine : ni CORS, ni
CSRF, ni durcissement de `CheckOrigin` à écrire, et l'ingress existant suffisait.
C'était la recommandation. **Rejetée** par choix d'exploitation en faveur d'un
artefact statique déployé séparément.

**Le jeton porteur gardé en mémoire JS.** Aucun changement backend pour le REST.
**Rejetée** : le flux WebSocket reste inatteignable — il faudrait passer le jeton
en query string, donc dans les logs d'accès, l'historique et le `Referer` — et le
jeton devient lisible par toute XSS. Pour un produit d'audit, c'est le mauvais
côté du compromis.

**Le jeton dans `Sec-WebSocket-Protocol`.** Évite la query string. **Rejetée** :
ne règle pas l'XSS, et ajoute une négociation de sous-protocole côté serveur pour
ne résoudre que la moitié du problème.

**Un `SameSite=Lax` par prudence.** **Rejeté** : l'UI ne joint l'API que par XHR,
jamais par navigation de premier niveau. `Strict` ne coûte donc rien ici et ferme
un cas de plus.

**Deux allowlists, une par consommateur.** Aurait permis d'ouvrir le CORS à un
outil interne sans ouvrir le flux. **Rejetée** : c'est exactement la
configuration où l'on croit avoir fermé une surface qu'on a laissée ouverte. Le
jour où ce besoin existera, il fera l'objet d'un ADR qui le nomme.

## Conséquences

- `WARDEN_UI_ORIGINS` non posée = comportement d'avant, la CLI intacte.
- `WARDEN_UI_COOKIE_INSECURE=1` retire `Secure` pour le développement local sur
  `http://localhost`. Nommé comme `WARDEN_INSECURE_DEV_AUTH`, et pour la même
  raison.
- L'ingress de l'API doit tenir le WebSocket : upgrade autorisé, timeouts de
  lecture longs (keepalive à 20 s, deadline à 5 min).
- `GET /runs` sérialise toujours `core.RunListItem` directement, donc en
  PascalCase et avec l'`ID` séquentiel. **Incohérence antérieure**, laissée en
  l'état parce que la CLI lit déjà cette forme ; à traiter par un ADR dédié.

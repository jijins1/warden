# ADR-0029 — La remontée d'events du runner : un seul message montant, qui clôt aussi le run

**Date** : 2026-08-16
**Statut** : Accepted
**Complète** : [ADR-0008](0008-postgresql-comme-journal-et-transport-devents.md), [ADR-0021](0021-modele-devent-normalise.md), [ADR-0017](0017-redaction-des-diffs-par-substitution-exacte.md)

## Contexte

Tout l'aval de l'ingestion existait déjà : `store.AppendEvents` insérait des lots
de façon idempotente sur `(run_id, seq)`, les triggers `warden_notify_events` et
`warden_notify_run_ended` étaient posés, `outbound/flux` écoutait, l'API servait
le WS et la CLI l'affichait. Le rôle PostgreSQL `warden_ingest` existait avec ses
GRANT.

Il manquait exactement un saut : **runner → ingestion**. Le runner s'arrêtait sur
une frontière posée exprès, juste après avoir matérialisé son code.

Un constat, découvert en instruisant la question, a élargi le sujet : **aucun code
de production n'appelait jamais `MarkRunning` ni `CloseRun`**. Tout run restait
`running` indéfiniment. Les deux méthodes existaient, testées, sans appelant.

## Décisions

### 1. Un seul message montant, qui porte aussi la clôture

La trame terminale voyage **dans** le lot d'events, pas sur un endpoint séparé :

```go
type Batch struct {
    Events []events.Event `json:"events,omitempty"`
    Final  *Final         `json:"final,omitempty"`
}
```

**Pourquoi c'est un choix de correction et pas de simplicité.** Le §6.3 exige que
les events soient persistés au fil de l'eau, « parce que la question d'audit qui
compte est *qu'est-ce qui avait déjà été modifié* quand un run est tué
brutalement ». Deux requêtes distinctes — un dernier lot, puis une clôture —
laisseraient une fenêtre où le run est déjà clos alors que ses derniers events ne
sont pas journalisés. C'est exactement l'état où cette question devient sans
réponse.

Côté magasin, `AppendAndClose` écrit les events **puis** la transition d'état,
dans la même transaction. L'ordre importe : un run clos dont les derniers events
manqueraient serait pire qu'un run non clos, qu'un réconciliateur peut rattraper.

Le lot ne porte **pas** de `run_id`. Le serveur résout `pod -> run` depuis le
jeton présenté (ADR-0023) ; l'ADR-0021 en fait une condition de validité du
schéma, l'`id` de run étant un entier séquentiel donc devinable.

### 2. Le rôle d'ingestion gagne un `UPDATE` étroit — écart assumé à l'ADR-0008

L'ADR-0008 écrivait « son propre rôle PostgreSQL restreint à `INSERT` sur la table
d'events ». La clôture par le runner exige plus. La migration `00006` donne :

```sql
GRANT UPDATE (state, termination_cause, ended_at,
              secrets_unmasked, secrets_unmasked_reason) ON run TO warden_ingest;
GRANT SELECT (ended_at) ON run TO warden_ingest;
```

Cinq colonnes, pas la table. Ni `audit_sha`, ni `user_id`, ni `run_day`, ni
`started_at` : ce sont les colonnes qui disent **qui a exécuté quoi**, et un pod
hostile qui pourrait les toucher réécrirait l'audit dont il est l'objet.

Le `SELECT (ended_at)` n'est pas décoratif : la clause `WHERE ended_at IS NULL`
qui rend la clôture idempotente **lit** cette colonne, et PostgreSQL exige le
SELECT même dans un `UPDATE`. Sans lui l'écriture échoue sur « permission denied
for table run », message qui ne nomme pas la colonne. C'est mesuré.

**Ce que ce GRANT ne borne pas, et il faut le dire** : `UPDATE run SET state`
sans clause `WHERE` reste permis. Ce qui enferme un pod dans *son* run n'est pas
le GRANT, c'est la résolution `pod -> run` côté serveur. Le GRANT est la seconde
couche, celle qui limite les dégâts si la première cédait.

### 3. `MarkRunning` est écrit par le control plane, pas par le pod

La transition vers `running` est posée par `POST /runs` juste après la création du
pod. C'est un fait que le control plane **constate**, avec son rôle applicatif —
pas la parole d'un pod, qui est du code hostile. Le §6.4 place d'ailleurs le
rapport du runner à la marche 2 de sa machine à états ; la marche 1 est cette
écriture-ci.

Les causes d'infrastructure — `pod_oom_killed`, `pod_evicted`, `node_lost` — ne
figurent **pas** dans le vocabulaire du contrat de fil, et leur absence est le
fait central : dans ces cas-là, le processus qui aurait dû les rapporter est mort.
Elles restent pour le watch informer du §6.4, donc pour le rôle `controller`. La
traduction fil → domaine est explicite et non un transtypage de chaînes,
précisément pour qu'un pod ne puisse pas se déclarer OOM-killed.

### 4. `ansible-runner worker` plutôt que `run -j` — mesuré

Trois interfaces ont été mesurées sur ansible-runner 2.4.3 / ansible-core 2.18.6
dans l'image du runner :

| Interface | Verdict |
|---|---|
| `run -j` (JSON sur stdout) | **rejetée** — flux INFIDÈLE |
| `run` + `artifacts/*/job_events/` | complet, mais impose de surveiller un répertoire |
| `transmit \| worker` | complet, mais fait transiter tout l'arbre audité en zip |
| **`worker --private-data-dir` seul** ✅ | complet, streamé, sans copie |

**`run -j` perd des events.** Les cinq events `verbose` d'un run trivial (les
compteurs 5 à 9) sortent sur stdout en **texte brut, pas en JSON** ; ils sont
pourtant bien présents dans `job_events/`. Un lecteur ligne à ligne qui saute ce
qu'il ne sait pas relire les perdrait en silence — et l'ADR-0013 ne purgeant rien,
la perte serait définitive.

Le `worker` accepte une trame `kwargs` de quelques centaines d'octets sur son
entrée standard et travaille sur un `--private-data-dir` **déjà peuplé** : pas
besoin de `transmit`, donc pas de zip de l'arbre audité dans un tube. Le flux est
du JSONL pur et complet, terminé par `{"zipfile": N}` suivi de N octets binaires
— sur lesquels la lecture s'arrête, sans quoi une archive deviendrait autant
d'events `unknown`.

**Conséquence de sécurité, mesurée aussi** : la trame `{"status": "starting"}`
contient `env`, c'est-à-dire **tout l'environnement du processus**. Le jour où
`forward_env` existera, elle contiendra les valeurs mêmes que l'ADR-0017 masque.
Les trames de contrôle ne deviennent donc jamais des payloads — seules les lignes
portant un champ `event` en produisent.

### 5. Le diff est **sorti** du payload

MESURÉ : le diff apparaît à deux endroits — `event_data.res.diff` sur un
`runner_on_ok`, et `event_data.diff` sur l'event `runner_on_file_diff` qu'Ansible
émet **en plus**, juste avant. Ne traiter que le premier laisserait le second
déposer le même contenu dans la table des events ordinaires.

C'est le seul endroit où le payload cesse d'être la ligne d'origine octet pour
octet, et l'écart est délibéré : l'ADR-0021 veut le payload intact, l'ADR-0008
veut les diffs dans une table à contrôle d'accès distinct, et pour les quelques
events qui portent un diff les deux ne peuvent pas être vrais en même temps. **La
séparation d'accès l'emporte**, parce qu'elle est une propriété de sécurité — la
laisser tomber rendrait décoratif tout `event_diff`. Le ré-encodage passe par
`json.Number`, sans quoi toute valeur numérique traverserait un `float64`.

### 6. Un échec ne compte jamais comme `changed`

MESURÉ, et c'est le piège du mapping : une commande qui **échoue** rapporte
`res.changed = true`. Un traducteur qui lirait le drapeau avant le type d'event
compterait des échecs comme des modifications — dans `changed_count`, qui est la
métrique d'audit principale du §6.5. `changed` n'est donc consulté que sur
`runner_on_ok`.

### 7. La rédaction de l'ADR-0017 est câblée dans `report`

C'est le dernier goulot avant que les octets quittent le pod, donc le bon endroit.
Elle s'applique au payload **et** au diff, fail-closed : une substitution qui
laisse quelque chose qui n'est plus du JSON valide fait abandonner le payload
plutôt que d'émettre l'original.

Le rédacteur est **nil aujourd'hui**, et c'est exact plutôt que négligent :
`core.OrderBuilder` ne remplit ni `ForwardEnv` ni `SSHKey`, donc aucun secret
n'est injecté dans le pod. La couture existe pour que le jour où l'un des deux
arrive, rien de ce chemin ne bouge.

`RedactRaw` a été ajouté à côté de `RedactJSON` pour la même raison qu'au point 5 :
décoder en `interface{}` puis ré-encoder réordonne les clés et arrondit les
nombres, ce qui réécrit silencieusement un résultat de module que l'audit doit
garder verbatim.

### 8. Deux corrections d'image, mesurées

`ansible-runner` **n'était pas dans l'image** — `apk add ansible-core` donne le
moteur, pas le flux d'events. C'était la dette nommée par l'ADR-0014 (« rien
n'impose que l'image contienne effectivement ansible-runner ») ; elle se
manifestait tard, au démarrage du run.

Et le répertoire personnel de l'utilisateur 65534 a été déplacé sur `/workspace` :
Ansible expanse le `~` de son `remote_tmp` depuis `/etc/passwd`, **pas depuis
`$HOME`**. Le compte `nobody` d'Alpine ayant `/` pour home, toute tâche en
`connection: local` tentait de créer `/.ansible/tmp` sur une racine en lecture
seule et rendait `unreachable`, avec un message parlant de permissions sur la
cible. Corriger l'entrée passwd plutôt que poser `ANSIBLE_REMOTE_TEMP` est
délibéré : cette variable s'appliquerait **aussi** aux hôtes gérés, où `~`
s'expanse chez la cible et où pointer sur `/workspace` n'aurait aucun sens.

## Conséquences

- **Avantages** :
  - la chaîne est complète de bout en bout : capture d'audit → push contrôlé →
    run → pod → ordre → code matérialisé → **exécution → events → clôture**, et
    le WS de la CLI, qui n'avait jamais rien eu à afficher, sert enfin ;
  - un run ne peut plus rester `running` indéfiniment ;
  - un run dont l'audit ne s'écrit plus s'arrête, ce que le §6.3 exigeait sans
    qu'aucun code ne l'applique ;
  - le traducteur est écrit contre des **fixtures mesurées**, versionnées : le
    jour où la version d'`ansible-runner` bouge, l'écart se voit en test plutôt
    qu'en production.

- **Contraintes** :
  - **Le périmètre d'hôtes du §10.2 reste absent** de la NetworkPolicy. Le
    playbook s'exécute réellement mais ne peut joindre aucune machine gérée : un
    run réussira sur `localhost` et échouera sur tout inventaire réel. C'est la
    bonne dégradation — une règle large donnerait à du code arbitraire l'accès de
    l'utilisateur le plus privilégié — mais c'est un trou connu.
  - Le déploiement de développement n'a **pas** d'utilisateur de connexion pour
    `warden_ingest` : l'ingestion retombe sur le DSN applicatif en le disant au
    démarrage. Le rôle restreint est testé à l'étage 2, il n'est pas en vigueur
    en `dev`.
  - Le rôle `ingest` est implémenté et **non déployé** : `api` sert le même
    endpoint sur le même port distinct. C'est ce que l'ADR-0008 voulait — sa
    promotion reste un changement de déploiement.

- **Dette technique éventuelle** :
  - L'escalade Ctrl-C à trois niveaux du §5.6 n'est traitée qu'au premier signal.
  - Les causes de terminaison d'infrastructure attendent le rôle `controller`.
  - `ANSIBLE_STDOUT_CALLBACK` est posé par `ansible-runner` lui-même
    (`awx_display`). Le jour où `forward_env` existera, l'allowlist du §5.4.1
    devra l'exclure : le laisser passer couperait le flux d'audit en silence,
    dette déjà nommée par l'ADR-0021.
  - Le volume réel d'events par run n'est toujours pas mesuré (dette ADR-0008).

# ADR-0031 — Réconcilier les runs tués : qui a le droit de prononcer une issue

**Date** : 2026-08-21
**Statut** : Accepted
**Complète** : [ADR-0006](0006-un-binaire-trois-roles.md), [ADR-0029](0029-remontee-devents-du-runner.md)

## Contexte

Le §6.4 décrit une machine à états en trois marches. Les deux premières
existaient : le run naît `interrupted` / `unknown` avant même que son pod existe
(ADR-0029), et le runner le clôt lui-même par l'ingestion quand tout se passe
bien. La troisième — « un watch informer sur les pods récupère
`containerStatuses[].lastState.terminated` » — n'avait aucune implémentation, et
`server.go` répondait `rôle "controller": pas encore implémenté`.

Ce que ça coûtait se lisait directement sur le cluster de développement : un run
`running` depuis trente-huit heures dont le pod n'existait plus, et une dizaine
de pods terminés qui s'accumulaient dans le namespace. Le produit d'audit
affirmait qu'un run était en cours alors qu'il était mort depuis deux jours.

## Décisions

### 1. Les FAITS sont extraits par l'adaptateur, la CAUSE est décidée par le cœur

`core.PodFacts` porte ce que le cluster dit — `status.reason`, la raison de
terminaison du conteneur, le code de sortie — et rien d'interprété.
`core.CauseDeTerminaison` traduit ces faits en cause du §6.4.

La séparation n'est pas décorative. La traduction est la moitié qui compte, et
c'est celle qui doit être vérifiable sans cluster ; l'extraction est la moitié où
une hypothèse sur Kubernetes peut être fausse, et elle est exportée
(`orchestrator.FaitsDuPod`) pour qu'un test puisse la viser directement.

L'ordre des cas est significatif : une éviction se manifeste AUSSI par un
conteneur terminé, un OOM par un code de sortie. Le fait le plus **spécifique**
l'emporte, sans quoi tout deviendrait `unknown`.

### 2. La clôture est GARDÉE par `ended_at IS NULL`

`store.CloseRunSiOuvert` n'écrit que si le run est encore ouvert, et rend si
l'écriture a eu lieu.

C'est ce qui rend le réconciliateur utilisable. Le runner clôt son propre run par
l'ingestion et son pod se termine juste après : **la course est permanente**. Sans
la garde, un passage remplacerait « succeeded » par « interrupted / pod_evicted »
sur un run parfaitement terminé — c'est-à-dire inscrirait un mensonge dans le
journal d'audit, précisément ce qu'il existe pour éviter.

La même garde porte l'idempotence que le §8.7 exige, et c'est elle qui autorise
la décision 5.

### 3. `unknown` reste fréquente, et c'est voulu

Un pod DISPARU donne `unknown`. Il a existé — `MarkRunning` suit sa création — et
rien de ce qu'on observe ne dit qui l'a fait disparaître. `admin_killed`
supposerait un acteur qu'on ne connaît pas, et le §6.4 pose que « la supprimer par
principe conduirait à inventer une cause plausible, bien pire en post-mortem ».
L'objectif est de la rendre rare, pas de l'éliminer.

Un conteneur terminé avec un code de sortie ZÉRO donne aussi `unknown` : le
processus est mort proprement, ce qui ne dit rien de ce que le playbook a fait.

### 4. Un LIST périodique, pas un informer

Le §8.7 parle d'un watch, et ce sera la bonne forme quand le volume l'exigera. Un
list toutes les trente secondes donne exactement la même réponse, ne peut pas
rater d'événement pendant une reconnexion, et n'introduit pas un cache dont la
fraîcheur devient une question. Le passage à l'informer ne changera pas ce que le
cœur voit.

**Toujours avec un sélecteur de labels.** Le réconciliateur SUPPRIME ce qu'il
ramasse : une liste non filtrée lui ferait supprimer des objets qui ne lui
appartiennent pas.

Une période de grâce de trente secondes protège de la liste, pas de
`MarkRunning` : un pod créé à l'instant peut ne pas encore apparaître, et le
conclure absent clôturerait un run parfaitement vivant.

### 5. Pas de leader election pour l'instant

L'ADR-0006 prévoit un Lease. Il ne changerait rien à la correction : la clôture
est gardée (décision 2) et le ramassage ne touche que des pods déjà terminés dont
plus aucun run n'est en vol. Deux contrôleurs feraient donc du travail en double,
pas du travail faux. Le déploiement pose `replicas: 1` et `strategy: Recreate`,
ce qui rend le doublon inutile en attendant.

### 6. Un runner qui MEURT ne prononce pas d'issue

C'est la décision que la mesure a imposée, et elle n'était pas prévue.

**Mesuré sur le cluster** : à l'expiration d'`activeDeadlineSeconds`, le pod passe
à `reason: DeadlineExceeded`, le signal annule le contexte du runner, celui-ci
envoie SIGINT à `ansible-runner` — qui rapporte alors `failed`, comme tout play
interrompu. Le runner clôturait donc le run en `failed`, et l'audit disait
**« votre playbook a échoué »** à propos d'un playbook qui n'avait rien dit.
Pire, en posant `ended_at` il devançait le réconciliateur, dont la garde
l'empêchait ensuite d'écrire la vraie cause.

Le runner rapporte désormais ce qu'il SAIT — ses events, ce qui avait déjà été
modifié (§6.3) — par un `report.Abandonner` qui vide le tampon **sans trame
terminale**, et laisse la clôture au réconciliateur, seul à voir
`DeadlineExceeded`, « Evicted » ou « OOMKilled ».

Un succès CONSTATÉ fait exception : si `ansible-runner` a rapporté `successful`,
le playbook est allé au bout et le signal est arrivé après. C'est un fait observé,
et le taire perdrait l'issue d'un run qui a réussi.

**Ce renvoi n'est sûr que parce que le contrôleur existe.** Avant lui, s'abstenir
aurait laissé le run en vol pour toujours. C'est la première fois que le produit
peut se permettre de ne pas conclure.

Corollaire : la branche qui posait `user_interrupt` sur un contexte annulé
disparaît. Un signal reçu par le pod vient de la plateforme, pas d'un humain ; le
Ctrl-C du §5.6 passe par `cancel_requested_at`, un autre chemin. Fabriquer un
signal fort — « quelqu'un a vu quelque chose de travers » — à partir d'un
événement d'infrastructure était une misattribution.

### 7. Le plafond de durée vient du déploiement

`WARDEN_RUN_DEADLINE_SECONDS` alimente `activeDeadlineSeconds`. Il existait en
configuration de l'orchestrateur sans que rien ne le renseigne, donc les deux
heures par défaut étaient inatteignables autrement qu'en recompilant. Une valeur
illisible fait échouer le démarrage plutôt que retomber sur le défaut : `30m`
écrit là où des secondes sont attendues donnerait silencieusement deux heures, et
personne ne le verrait avant d'avoir besoin du plafond.

## Conséquences

**Ce qui est vérifié**, sur le cluster réel et pas seulement en test :

- un run dont le pod est tué en plein vol est clos en **6 secondes**, avec
  `changed_count = 1` — l'audit conserve ce qui avait déjà été modifié ;
- un run arrêté sur le plafond de durée est clos en `interrupted` /
  `server_task_timeout`, et ses treize events sont tous là ;
- les onze pods terminés qui traînaient ont été ramassés au premier passage, avec
  leurs Secrets.

**Ce que ça coûte** :

- un run tué reste `running` jusqu'à une minute (grâce + intervalle) au lieu
  d'être clos immédiatement par le runner. C'est le prix de la décision 6, et il
  achète une cause juste contre une cause fausse ;
- si le contrôleur est arrêté, les runs tués restent en vol jusqu'à son retour.
  Il se rattrape au démarrage, un passage ayant lieu avant le premier tick ;
- un playbook qui échoue à la milliseconde près avant un signal sera enregistré
  `interrupted` plutôt que `failed`. Les events gardent les tâches en échec, et le
  plafond a bel et bien expiré.

**Ce qui reste ouvert** : le Lease de l'ADR-0006, l'informer du §8.7, et les
causes `admin_killed` et `client_disconnect_timeout` qui n'ont toujours aucun
producteur.

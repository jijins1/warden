# ADR-0027 — L'écriture du rôle `git` : quarantaine native, fraîcheur des objets, ref d'audit écrite une fois

**Date** : 2026-08-14
**Statut** : Accepted
**Amende** : [ADR-0026](0026-isolation-de-lecture-du-role-git.md),
[ADR-0025](0025-perimetre-de-la-capture-daudit.md),
[ADR-0006](0006-un-binaire-trois-roles.md)

## Contexte

L'[ADR-0026](0026-isolation-de-lecture-du-role-git.md) a livré le smart HTTP en
lecture et s'est terminée sur une dette nommée :

> Le point 2 de la conjonction (refus de `receive-pack`) est aujourd'hui obtenu
> par l'**absence de la fonctionnalité**. Quand la quarantaine l'ouvrira, il
> faudra qu'un runner reste incapable d'écrire une ref dans son espace de noms —
> sans quoi le vecteur n°1 se rouvre.

Cette ADR ouvre l'écriture et solde cette dette. Elle en découvre une autre au
passage, plus large que prévu.

**Toutes les affirmations sur le comportement de git ci-dessous sont MESURÉES**,
sur git 2.43.0, par les sondes de `internal/server/internal/gitd/receive_probes_test.go`,
conservées dans l'arbre comme garde-fous de montée de version. L'ADR-0026 a
appris ce que coûte le raisonnement sur la documentation seule : trois hypothèses
testées, deux fausses, et les deux échouaient en silence.

### Le modèle git a été simplifié en amont

Décision produit prise avant cette ADR, et qui conditionne tout ce qui suit :

> Les exécutions protégées en PROD partent **toujours** de la branche principale.
> Toutes les autres branches sont traitées **comme des commits locaux**.

Côté serveur, il n'existe donc que deux choses : **la branche principale**, seule
ref que le serveur connaisse, et **tout le reste**, indistinctement du commit
local qui arrive avec le push. C'est ce qui rend le contrôle de fraîcheur
ci-dessous simple au lieu d'être un ensemble calculé.

**Conséquence à assumer** : l'ADR-0003 traite « le premier push d'une branche
locale divergée » comme un cas limite au coût non borné. Ce n'est plus un cas
limite, c'est le **cas normal**. Le seuil `WarningThreshold` de
`internal/cli/internal/upload/push.go` se déclenchera en routine, et le budget du
scan se dimensionne sur ce cas, pas sur un delta d'un commit.

## Constat — le vecteur n°1 a une seconde forme, et elle est pire

L'ADR-0026 citait le premier vecteur de `gitnamespaces(7)` : « l'attaquant envoie
une ref vers X sans envoyer X ; la victime croit qu'il détient X et le lui
renvoie plus tard ». **Mesuré, sonde A3**, en configuration par défaut :

```
POST /depot.git/git-receive-pack
  pkt-line : 000…0 <sha de la victime> refs/heads/stolen\0report-status
  pack     : PACK, version 2, 0 objet

→ 200, « unpack ok », « ok refs/heads/stolen »
→ la ref est créée, pointant sur un objet que le client n'a jamais envoyé
```

Un `git push` standard ne sait pas formuler cela — il refuse de nommer un objet
qu'il ne détient pas. La requête a donc été forgée. Ce n'est pas une atténuation :
le §2 pose que le modèle de menace est du code arbitraire, et quarante octets de
pkt-line ne sont pas un obstacle.

**La seconde forme est celle qu'un contrôle sur le seul commit de tête laisse
passer**, et elle n'a besoin d'aucune requête forgée :

> Alice pousse un commit d'audit **neuf**, dont l'arbre est celui du commit
> d'audit de Bob. La tête est nouvelle, donc transmise. L'arbre, lui, ne l'est
> pas : Alice ne l'a jamais eu, elle n'en connaît que le SHA — et l'ADR-0026 a
> acté qu'un SHA n'est pas un secret. Le push est accepté. La ref d'audit d'Alice
> pointe sur le code de Bob, qu'Alice lit ensuite par le diff de **son propre**
> run, auquel la politique d'accès lui donne légitimement droit.

Ni la quarantaine, ni l'immutabilité, ni un test de fraîcheur de la tête ne
ferment cela.

**Conséquence structurante : la propriété d'isolation en LECTURE de l'ADR-0026
dépend désormais aussi d'un contrôle situé sur l'arête d'ÉCRITURE.** La
conjonction passe de trois termes à cinq.

## Solutions envisagées

### Le mécanisme de quarantaine

#### Option A2 — dépôt bare temporaire, puis promotion
Recevoir dans un dépôt jetable, scanner, puis transférer les objets.
- **Rejetée parce que** : le client reçoit `report-status` — donc « ok » — **avant**
  la promotion. Un push peut être annoncé réussi puis échouer au transfert, et
  l'audit affirmerait l'existence d'un artefact absent. Sur un produit dont toute
  la valeur est « ce qui s'est exécuté correspond exactement au commit journalisé »,
  c'est disqualifiant. Corriger imposerait de réécrire les lignes de statut du
  protocole, ce que l'ADR-0005 interdit.

#### Option A1 — `GIT_QUARANTINE_PATH` natif + hook `pre-receive` ✅ (retenue)
- **Choisie parce que** : la garantie est celle de git, et elle est **mesurée**
  (sonde A2) — un `pre-receive` non nul laisse le magasin d'objets rigoureusement
  inchangé, sans résidu `incoming-*`, et la ref n'est pas créée. La négociation
  reste optimale, et la promotion est un renommage intra-système de fichiers.

**Ce qu'elle coûte, et qu'il faut écrire** : il faut des hooks, donc revenir sur
`core.hooksPath=/dev/null`, qui est aujourd'hui un invariant de durcissement
(`internal/git/exec.go`, `smarthttp.go`). La neutralisation reste en vigueur
**partout ailleurs**. Sur cette seule arête, `core.hooksPath` pointe vers un
répertoire **détenu par le rôle**, hors de la racine des dépôts, posé par
variable d'environnement — donc non surchargeable par la config d'un dépôt. Un
`hooks/` déposé dans un dépôt bare ne doit rien pouvoir exécuter, et un test
négatif le vérifie.

### Le périmètre du scan de secrets

- **Rejeté — l'arbre du commit d'audit** : ne couvre pas l'historique local que
  l'ADR-0003 fait transiter, et rescanne éternellement des blobs déjà acceptés.
  Un secret présent dans la principale bloquerait alors tous les runs, pour
  toujours.
- **Rejeté — le diff base → audit** : même trou, et un diff ne dit rien des blobs
  atteignables non modifiés.
- **Retenu ✅ — `git rev-list --objects <nouvelle valeur> --not <principale>`.**

Cette formulation donne **deux propriétés d'un seul calcul** : l'ensemble exact
des objets que le push ajoute à ce que le pousseur pouvait déjà voir — le
périmètre de scan que l'ADR-0003 demande, ni plus ni moins — et l'ensemble sur
lequel se vérifie la fraîcheur.

### L'immutabilité de la ref d'audit

- **Rejeté — hook `update`** : `receive-pack` migre les objets **dès que
  `pre-receive` retourne 0**, donc avant `update`. Un refus en `update` laisse
  les objets dans le dépôt. Le refus doit être en `pre-receive` ou il n'a pas la
  sémantique qu'on croit.
- **Insuffisant seul — configuration** (`receive.denyDeletes`,
  `receive.denyNonFastForwards`) : autorise l'avance rapide, c'est-à-dire
  exactement « ajouter un commit à la ref qui documente un run déjà journalisé ».
- **Retenu ✅ — `pre-receive` + configuration**, le hook portant le contrôle et la
  configuration couvrant tout chemin futur qui ne passerait pas par lui.

## Décision

### 1. L'isolation d'un run est une conjonction de cinq contrôles

Aucun ne suffit seul. L'ADR-0026 en énonçait trois ; les deux derniers sont
apportés par cette ADR.

| # | Contrôle | Ce qu'il ferme |
|---|---|---|
| 1 | `GIT_NAMESPACE` | La découverte des refs d'autrui. Pas l'accès |
| 2 | Protocole épinglé en v0 | Le `want` sur un SHA non annoncé (ADR-0026) |
| 3 | Listener, port et port d'autorisation distincts pour l'écriture | Le runner, qui ne joint pas cette surface et dont le jeton y est inconnu |
| 4 | Allowlist de refs en `pre-receive` | Le pousseur légitime qui vise ailleurs |
| 5 | **Contrôle de fraîcheur** | Le pousseur légitime qui **nomme** un objet qu'il n'a pas envoyé |

### 2. Énoncé exact du contrôle de fraîcheur

> Tout objet atteignable depuis la nouvelle valeur de la ref, et non atteignable
> depuis la branche principale, doit avoir été **transmis par ce push**.

Mise en œuvre en deux invocations, l'une et l'autre reposant sur des variables
d'environnement documentées et **jamais** sur la disposition interne de la
quarantaine — point vérifié par la sonde A1 :

| | Environnement | Ce que git voit |
|---|---|---|
| Énumération | celui du hook | quarantaine (`GIT_OBJECT_DIRECTORY`) **et** magasin (`GIT_ALTERNATE_OBJECT_DIRECTORIES`) |
| Présence | `GIT_OBJECT_DIRECTORY`, `GIT_ALTERNATE_OBJECT_DIRECTORIES` et `GIT_QUARANTINE_PATH` retirées | le magasin **seul** |

Un objet énuméré et déjà présent dans le magasin seul → refus : « ce push
revendique un objet qu'il n'a pas envoyé ».

### 3. La branche principale est l'unique ancre, et le serveur seul l'écrit

Le `--not` du contrôle est la principale. **Elle doit donc être écrite par le
serveur, depuis une source que le client ne contrôle pas**, sans quoi l'ancre
devient l'attaque : un client qui déclare comme principale un commit contenant
l'arbre d'autrui l'exclut du périmètre, n'a plus à le transmettre, et échappe du
même coup au scan de secrets.

**Le rôle `git` rafraîchit le miroir de la principale par `fetch` depuis la
forge, à la réservation du run.** L'URL du dépôt et le nom de la branche protégée
sont de la **configuration serveur**, jamais du fichier client — c'est la
décision actée n°7, « le fichier client est une requête ; le serveur détient la
politique ». `git.ParseRemote` s'applique à ce fetch : il refuse déjà tout ce qui
n'est pas http/https, donc `ext::` et `file://`, les deux transports qui exécutent
du code.

Les deux usages du miroir n'ont **pas le même mode de défaillance**, et le choix
est explicite :

| Usage | Miroir périmé | Politique |
|---|---|---|
| Contrôle de fraîcheur | sûr — un `--not` plus petit rend le contrôle plus strict | tolérant |
| Ancestry PROD (§3.2) | faux — on validerait contre un état dépassé | **fail-closed** |

### 4. La ref d'audit est écrite une fois

Refus en `pre-receive` de toute mise à jour dont l'ancienne valeur n'est pas
nulle sous `refs/audit/`, complété par `receive.denyDeletes`,
`receive.denyNonFastForwards` et `receive.hideRefs`.

**Ce que la propriété ne dit pas** : ce n'est pas de l'inviolabilité. Le §6.2
pose déjà qu'un dépôt git n'est pas *tamper-evident* face à un administrateur, et
la maintenance de l'ADR-0004 conserve le droit de supprimer des refs. L'énoncé
exact est : **aucun client du smart HTTP ne peut écraser ni supprimer une ref
d'audit.**

Mesuré (sonde A4) : `receive.hideRefs` masque de l'annonce **et refuse
l'écriture** — `deny updating a hidden ref` — identiquement en v0 et v2. C'est
une seconde ligne de défense qui n'était pas escomptée. Le hook reste le contrôle
principal ; `hideRefs` ne doit pas porter la propriété à lui seul.

### 5. Ports d'autorisation séparés

L'écriture a son propre port `PushAuthorizer`, son propre handler et son propre
listener, dans la continuité de l'ADR-0024. La raison est celle qui a déjà servi
deux fois : avec un simple booléen `CanWrite` dans le `Scope` existant,
« un runner ne peut pas écrire » deviendrait la propriété d'une **implémentation**
du port, qu'un calcul erroné rouvrirait. Séparés, c'est une propriété du **chemin
de code et de la topologie**. Les deux appelants sont d'ailleurs réellement
distincts : le runner est intra-cluster, la CLI est un poste de développement.

Comme pour `Scope.Namespace`, un `PushScope` sans ref échoue **fermé**.

### 6. Le scan de secrets utilise gitleaks en bibliothèque

Réutiliser plutôt qu'écrire. Trois règles non négociables :

1. **Échec fermé.** Un push trop gros pour être scanné dans le budget est
   **refusé**, jamais accepté non scanné. Le §5.3 dit « obligatoire », et
   l'ADR-0013 rappelle qu'il n'y a aucun filet temporel derrière.
2. **La trouvaille est un secret.** Ni journal, ni réponse au client, ni trace. Le
   message nomme la règle, le fichier et la ligne — **jamais la valeur**.
3. **Pas de fichier d'exclusion côté client.** C'est la leçon Atlantis du §5.4 :
   le fichier du client est une requête, pas une déclaration. Configuration et
   allowlist sont serveur ; une exception est une action d'opérateur. En pratique
   le jeu de règles embarqué est utilisé tel quel, `sources.File.Config` est
   laissé nul — le renseigner activerait le `shouldSkipPath` de gitleaks, dont
   l'allowlist dispenserait un chemin d'un scan que le §5.3 dit obligatoire — et
   `MaxArchiveDepth` vaut zéro : la profondeur d'imbrication d'une archive est
   contrôlée par l'attaquant, quelques kilo-octets sur le fil pour un parcours
   arbitrairement coûteux.

**Trois notes d'implémentation qui ont coûté du temps et méritent d'être
écrites :**

- `detect.NewDetectorDefaultConfig` lit et écrit le **singleton viper du
  paquet**. Le produit construit donc sa propre instance `viper.New()` : muter un
  état global depuis un appel de bibliothèque marche jusqu'au jour où autre chose
  dans le processus utilise viper.
- `DetectReader` est **déprécié** dans la version épinglée, et la boucle
  `Fragments` manuelle impose la conversion `detect.Fragment`, dépréciée elle
  aussi. La seule voie courante est `DetectSource`.
- Le jeu de règles par défaut **ne signale pas** une clé AWS `AKIA…` seule, et
  met explicitement en allowlist l'exemple `AKIAIOSFODNN7EXAMPLE` de la
  documentation d'Amazon. Une suite de tests bâtie sur ces valeurs échoue alors
  que le scanner fonctionne. `TestTheShippedRuleSetActuallyDetects` existe pour
  la panne inverse, bien plus grave : si la configuration embarquée cessait de se
  charger, `New` rendrait quand même un scanner et `Scan` ne trouverait plus
  jamais rien — un produit vert, silencieux et sans protection.

## Conséquences

- **Avantages** :
  - la dette de l'ADR-0026 est soldée, et la propriété est énoncée en termes
    exacts donc vérifiables ;
  - le modèle git simplifié rend l'ancre du contrôle **constante** au lieu d'être
    un ensemble calculé, ce qui supprime une classe entière d'erreurs ;
  - les cinq contrôles sont épinglés par des tests d'étage 1, chacun échouant si
    l'on retire un seul terme.

- **Contraintes** :
  - **Le rôle `git` acquiert un egress sortant vers la forge.** L'ADR-0006 disait
    « pendant son indisponibilité, aucun run ne démarre dans l'organisation » ;
    cette phrase couvre maintenant aussi la disponibilité de la forge. C'est un
    élargissement réel du point de défaillance unique.
  - Des credentials de lecture sur la forge sont nécessaires, dans un
    environnement dont l'ADR-0020 a constaté qu'il n'a pas de gestionnaire de
    secrets.
  - **`receive.fsckObjects` refuse des dépôts légitimes.** Mesuré (sonde A5) : un
    commit portant `missingSpaceBeforeEmail` — production courante de vieux
    outils — fait échouer le push. Comme l'historique local est désormais le cas
    normal, le symptôme serait « warden refuse d'auditer mon dépôt ». Il faut une
    **table de sévérités instruite** : garder en `error` ce qui porte de la
    sécurité (`gitmodulesPath`, `hasDotgit`, `hasDotdot`, liens symboliques
    d'attributs), passer les défauts cosmétiques en `warn`. Ne pas désactiver
    fsck : le runner matérialise cet arbre.
  - Le port d'écriture reste joignable depuis les pods runner tant qu'aucune
    NetworkPolicy n'existe — même réserve que l'ADR-0024. Les contrôles 3, 4 et 5
    restent vrais sans elle ; la séparation par port est une condition de
    déploiement, pas une garantie du code.
  - Mesures faites sur git 2.43.0 alors que l'ADR-0005 exige 2.45. Les sondes
    restent dans l'arbre pour signaler tout écart à la montée de version.

- **Dette technique éventuelle** :
  - **`receive.fsckObjects` n'est pas activé**, alors que le constat A5 le
    justifie et que la configuration le prévoit. Il attend la table de sévérités
    instruite décrite plus haut : l'activer sans elle refuserait des dépôts
    légitimes, et l'historique local est désormais le cas normal.
  - Le chemin d'écriture ne doit **pas** être câblé tant que le miroir de la
    principale n'est pas alimenté par le serveur. Jusque-là, l'absence de câblage
    est ce qui tient la propriété — la même posture que l'ADR-0026 a tenue pour
    `receive-pack`.
  - La réservation du run n'existe pas : le §5.2 ordonne « push d'audit puis
    `POST /runs` », alors qu'un jeton scopé à une ref suppose que le serveur
    connaisse cette ref avant le push. Il manque une étape de réservation, à
    traiter avec le miroir.
  - Aucun quota par utilisateur ne borne l'occupation du PVC en instance unique.
    Assumé, et à reprendre avec les métriques du point 2 de l'ADR-0013.
  - Le délai de grâce du `prune` de l'ADR-0013 était plancher-é sur une
    quarantaine *dans* le dépôt. Le mécanisme natif place les objets hors du
    graphe : le raisonnement est à relire, et l'interaction entre `git maintenance`
    (ADR-0004) et un `tmp_objdir-incoming-*` en vol reste à vérifier.

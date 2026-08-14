# ADR-0025 — Périmètre de la capture d'audit : l'arbre dit ce qui va tourner

**Date** : 2026-08-08
**Statut** : Accepted

## Contexte

Le §5.3 tranche le *quoi* — « fichiers suivis + fichiers non suivis non ignorés,
plus une liste d'inclusions explicites », avec un `.auditignore` distinct du
`.gitignore` — et l'ADR-0005 tranche le *comment* : `GIT_DIR` temporaire,
`objects/info/alternates` vers le magasin de l'utilisateur, `GIT_INDEX_FILE`
temporaire.

Ce qu'aucun des deux ne dit, et qui apparaît dès qu'on écrit le code, c'est ce
que l'arbre d'audit **signifie**. Trois questions restaient ouvertes, et chacune
a une réponse qui change ce que l'auditeur lit.

## Solutions envisagées

### L'index d'audit est-il semé avec l'arbre de base ?

Le squelette écrit en phase 2 décrivait la séquence `read-tree <base_sha>` puis
`update-index --add` du périmètre. C'est la démarche naturelle si l'on pense
l'audit comme un delta appliqué à la base.

**Retenu — l'index est construit à partir de rien.** L'arbre d'audit contient le
périmètre, exactement, et rien d'autre.

**Écarté — semer avec `read-tree <base>`.** Un fichier supprimé du worktree et
non indexé resterait présent dans l'arbre, parce qu'il est toujours dans la
base. Le runner matérialise cet arbre : il exécuterait donc un fichier que
l'utilisateur a supprimé. Ce n'est pas une imprécision d'affichage, c'est un
écart entre ce qui est audité et ce qui tourne — la seule chose que ce produit
ne peut pas se permettre.

Corollaire de plomberie, vérifié plutôt que supposé : le périmètre vient de
`ls-files --cached`, qui liste encore un fichier effacé du disque tant que la
suppression n'est pas indexée. `update-index --add` s'arrête alors en sortie 128
sur `does not exist and --remove not passed`. C'est `--remove` qui fait tomber le
chemin au lieu de faire échouer la capture, et le cas est courant : personne ne
fait `git rm`.

### `.auditignore` s'applique-t-il aux fichiers suivis ?

**Retenu — non. Il ne filtre que les fichiers non suivis.** Un fichier suivi est
toujours dans le périmètre.

La raison est une ambiguïté de lecture qu'aucun outil ne peut lever après coup :
**dans un arbre, un fichier exclu du périmètre est indiscernable d'un fichier
supprimé.** Les deux produisent le même arbre, donc la même ligne de suppression
dans les diffs du §6.5. L'auditeur n'a aucun moyen de distinguer « le projet a
choisi de ne pas auditer ce fichier » de « ce fichier a disparu ».

**Écarté — permettre l'exclusion, et enregistrer le périmètre comme métadonnée
du run** pour que l'interface distingue les deux cas. C'est faisable, mais cela
déplace une propriété de l'artefact vers la base applicative : une ref d'audit
consultée avec un outil git ordinaire redeviendrait trompeuse, ce que
l'ADR-0003 a précisément refusé en écartant le commit orphelin.

Restreindre `.auditignore` aux fichiers non suivis supprime le cas par
construction, et ne coûte rien : le §5.3 présente ce fichier comme le mécanisme
qui **fait entrer** l'inventaire et les vaults que `.gitignore` exclut, pas comme
un mécanisme de retrait.

En revanche `.auditignore` **remplace** les sources d'exclusion de git au lieu de
s'y ajouter. S'il s'y ajoutait, l'inventaire resterait exclu par le `.gitignore`
du projet et le fichier ne servirait à rien.

### Les filtres `clean` s'appliquent-ils au contenu capturé ?

**Retenu — non, le contenu est capturé brut.** Le `GIT_DIR` d'appoint ne porte
pas la configuration du dépôt, donc `filter.*.clean` ne s'exécute pas.

C'est un écart assumé à la promesse de « sémantiques exactes » qui justifiait le
binaire git à l'ADR-0005, et c'est le bon sens dans lequel se tromper : le runner
n'a pas non plus de configuration de filtre. Brut à l'entrée, brut à la sortie,
le passage est exact. Un contenu nettoyé exigerait un `smudge` de l'autre côté
que rien n'exécute, et produirait dans le pod un fichier différent de celui que
l'utilisateur a sur son disque.

Le cas `git-lfs` reste traité à part, et refusé : un pointeur capturé à la place
de son contenu produit un audit qui a l'air complet et ne l'est pas. C'est la
dette que l'ADR-0005 et l'ADR-0007 signalent tous les deux, et elle est ici
détectée et refusée avec un message actionnable.

## Décision

| Sujet | Décision |
|---|---|
| Composition de l'arbre | Index construit à partir de rien. L'arbre est le périmètre, exactement. |
| Fichier supprimé non indexé | Absent de l'arbre. `update-index --add --remove`. |
| `.auditignore` sur les fichiers suivis | Sans effet. Les fichiers suivis sont toujours audités. |
| `.auditignore` sur les non suivis | Remplace les règles d'exclusion de git, ne s'y ajoute pas. |
| Inclusions explicites | Rattrapent des chemins nommés. Sans pathspec, elles n'ajoutent rien. |
| Contenu | Brut. Pas de filtre `clean`. |
| Pointeurs `git-lfs` | Refusés, avec la commande qui débloque. |
| Ordre du périmètre | Trié et dédoublonné, pour que l'arbre soit reproductible. |

**Le push part du magasin d'appoint, pas du dépôt de l'utilisateur.** Ce n'est
pas un détail d'implémentation : le commit d'audit n'existe nulle part ailleurs,
puisque toute l'isolation consiste à ne rien écrire chez l'utilisateur. Le lien
`alternates` reste ouvert pendant le push, ce qui permet à la négociation de ne
transmettre que le delta. Conséquence sur la durée de vie : le magasin d'appoint
se ferme **après** l'envoi, jamais après le commit.

## Conséquences

L'invariant du §5.2 — « l'index et le HEAD de l'utilisateur ne sont jamais
modifiés » — est vérifié **à chaque capture**, pas une fois dans un test. L'état
est relevé avant et après, et une divergence fait échouer le run même si l'arbre
produit est bon. Un audit correct obtenu en salissant le dépôt de quelqu'un est
un échec produit.

Le relevé porte sur quatre choses, dont une qui ne va pas de soi : l'empreinte du
**fichier** d'index, et non ce que `git status` en rapporte. Le status est dérivé
de l'index et resterait d'accord avec lui-même si l'index avait été réécrit à
l'identique avec un nouvel horodatage — or c'est exactement ce que l'utilisateur
verrait, son éditeur rafraîchissant son intégration git sans raison.

Un cinquième contrôle n'est pas dans le code de production mais dans les tests :
le nombre d'objets sous `.git/objects` de l'utilisateur, avant et après. C'est la
seule mesure qui prouve qu'aucun objet loose n'a fui, et elle est trop coûteuse
sur un grand dépôt pour tourner à chaque run.

La capture est **reproductible** : mêmes fichiers, même base, même signature
donnent le même SHA. Les dates de l'auteur et du committer sont fournies plutôt
que prises à l'horloge, sans quoi deux captures identiques produiraient deux
commits différents. C'est ce qui rend une reprise après coupure réseau sans
conséquence — le push rejoué présente le même SHA et le serveur le déclare déjà
à jour.

Ce que cette reproductibilité **ne donne pas**, c'est l'immuabilité. Un commit
différent poussé sur la même ref est refusé aujourd'hui, mais seulement parce
qu'il n'est pas en avance rapide — un effet de bord des défauts de git, pas une
garantie. Rendre une ref d'audit écrite-une-fois est une propriété du rôle `git`
côté serveur. **Tranchée depuis par l'[ADR-0027](0027-ecriture-du-role-git.md)** :
refus en `pre-receive` de toute mise à jour dont l'ancienne valeur n'est pas
nulle sous `refs/audit/`, complété par `receive.denyDeletes`,
`receive.denyNonFastForwards` et `receive.hideRefs`. Le code côté client ne
prétend toujours rien : il s'abstient de `--force` et le dit.

Enfin, deux points de sécurité que cette tranche a fermés en passant, parce que
la capture est le premier endroit où le produit exécute git **dans un dépôt qu'il
n'a pas créé** :

- `core.hooksPath` et `core.fsmonitor` sont neutralisés à chaque invocation.
  Neutraliser `GIT_CONFIG_GLOBAL` et `GIT_CONFIG_SYSTEM` ne suffisait pas : le
  `.git/config` du dépôt reste lu, et ces deux clés y désignent des programmes
  que git lance pour nous — `fsmonitor` jusque sur les commandes en lecture.
- L'URL du serveur n'accepte que `http` et `https`. Une URL de remote git n'est
  pas seulement une adresse : le transport `ext::` prend une commande shell et
  l'exécute. Cette URL vient de la configuration projet (§5.4), donc d'un fichier
  versionné — une entrée non fiable au sens du §2.

Le jeton d'authentification voyage dans l'**environnement** et non dans `argv`.
L'ADR-0005 nomme `-c http.extraHeader` ; `GIT_CONFIG_COUNT` et ses paires
numérotées y sont exactement équivalents, à une différence près qui décide du
choix : une ligne de commande est lisible par tout utilisateur de la machine via
`/proc`, un environnement ne l'est pas. La CLI tourne sur un poste de
développement, c'est-à-dire précisément là où « tout utilisateur de la machine »
n'est pas une hypothèse d'école.

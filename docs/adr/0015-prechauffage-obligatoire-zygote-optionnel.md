# ADR-0015 — Le pré-chauffage est obligatoire, le zygote reste optionnel

**Date** : 2026-08-05
**Statut** : Accepted

## Contexte

Le §3.1 et le §5.1 promettent une boucle de développement à **moins de 3
secondes de surcoût**. Toute l'UX du produit repose là-dessus : l'utilisateur
tape presque `ansible-playbook` et ne doit pas sentir la différence.

Le §12 place donc la phase 0 en premier — mesurer le cold start avant d'écrire
le produit — avec un critère de sortie explicite : « si le pré-chauffage seul ne
descend pas sous ~3 s, le zygote (§8.5) devient obligatoire et non optionnel ».

Le zygote est l'optimisation la plus lourde de la spec : un processus Ansible
pré-initialisé qu'on forke, par analogie avec le zygote d'Android. Personne ne
veut le construire au cas où.

### Protocole

Cluster k3s v1.36.1, nœud `muzi` (amd64, Ubuntu 24.04, containerd 2.2.3). Pod
épinglé sur ce nœud par `nodeName`, qui est aussi la machine de mesure — les
horloges sont donc les mêmes, ce qui autorise à comparer un timestamp pris
dehors et un timestamp pris dans le conteneur.

Image `quay.io/ansible/ansible-runner:latest`, 320 Mo. Playbook réduit à une
tâche `debug`, `gather_facts: false`, `connection: local` : aucun hôte cible,
aucun SSH, aucun fait — la mesure porte sur le surcoût du proxy et sur lui seul.

Les postes fins sont mesurés **dans** le conteneur, par marqueurs
`date +%s%N`. C'est nécessaire : les timestamps de l'API Kubernetes sont à la
seconde, ce qui est inutilisable face à un budget de 3 s.

### Mesures

| Poste | Valeur | n |
|---|---|---|
| Total à froid, pull compris | 20 501 ms | 1 |
| Total à chaud (`kubectl run --rm --attach`) | méd. 3 072, p95 4 057 ms | 20 |
| Le même pod exécutant `/bin/true` | méd. 3 033 ms | 10 |
| Création du pod → première instruction dans le conteneur | 1 032 à 1 660, moy. 1 299 ms | 10 |
| Fin d'exécution → retour de `kubectl` (teardown) | moy. 1 900 ms | 10 |
| `kubectl` seul (appel API sans pod) | 122 à 163 ms | 5 |
| Python nu | 19 ms | 1 |
| Python + `import ansible` | 119 ms | 1 |
| `ansible-playbook`, play vide, une tâche | 799 / 810 / 779 ms | 3 |
| `ansible-runner --version` | 298 ms | 1 |
| `git clone --depth 1` d'un dépôt de 32 Mo depuis Internet | 1 375 ms | 1 |

### Lecture

**Le teardown de 1,9 s est un artefact.** `kubectl --rm --attach` attend la
suppression du pod ; le produit ne l'attend pas, sa CLI suit un flux WebSocket.
Il ne figure donc pas dans le surcoût perçu.

**Le coût d'Ansible n'est pas l'import Python.** 119 ms pour charger le module,
mais ~795 ms pour exécuter un play qui ne fait rien : la dépense est dans la
découverte des plugins, le parsing du play et la mise en place de la stratégie.
Une optimisation qui viserait le démarrage de l'interpréteur se tromperait de
cible.

**Chemin réaliste sur nœud chaud, sans pré-chauffage :**

```
1,3 s   création du pod
0,2 s   fetch git (delta local — le 1,375 s mesuré porte sur 32 Mo depuis Internet)
0,8 s   démarrage d'ansible-playbook
─────
2,3 s   avant la première tâche réelle
```

Contre un budget de 3 s. Ça tient de justesse, et n'importe quel playbook réel —
collections, collecte de faits — le dépasse.

**Avec le pré-chauffage**, le terme dominant disparaît : **~1,1 s**.

### Note de méthode

La première tentative comparait deux pods — l'un lançant Ansible, l'autre
`/bin/true` — pour en déduire le coût d'Ansible par différence. Elle a donné
~40 ms, valeur fausse : la variance du cycle de vie d'un pod est de ±1 s, une
différence de 795 ms y est invisible. **Toute mesure de l'ordre de la seconde
doit être prise à l'intérieur du conteneur**, jamais par différence entre deux
lancements de pod.

## Solutions envisagées

### Option A — Construire le zygote maintenant
Un processus Ansible pré-initialisé, forké à chaque run.
- **Avantage** : c'est la seule voie vers « sous la seconde », et le §8.5 la
  présente comme le vrai différenciateur face à AWX.
- **Rejetée parce que** : la mesure montre que le pré-chauffage seul remplit le
  critère de sortie du §12 avec de la marge. Construire d'abord la pièce la plus
  difficile pour gagner 795 ms sur un budget déjà tenu à 1,1 s, c'est payer très
  cher un gain qu'on n'a pas encore besoin d'aller chercher.

### Option B — Ni pool ni zygote, accepter le démarrage à froid
- **Rejetée parce que** : 2,3 s de plancher mesuré, sur un playbook vide, sans
  collection et sans hôte distant. Le budget de 3 s serait consommé avant la
  première tâche utile de n'importe quel projet réel.

### Option C — Pré-chauffage maintenant, zygote différé ✅ (retenue)
- **Choisie parce que** : elle attaque le terme dominant — la création du pod,
  1,3 s des 2,3 s — avec le mécanisme que le §8.3 décrit déjà, et laisse de côté
  la seule pièce dont la complexité soit hors de proportion avec son gain.

## Décision

**Le pré-chauffage (§8.3) est obligatoire.** Ce n'est pas une optimisation de
confort : sans lui, le budget du §3.1 est consommé avant la première tâche.

**Le zygote (§8.5) reste optionnel.** Le critère de sortie de la phase 0 est
rempli — le pré-chauffage seul descend à ~1,1 s, largement sous les 3 s.

**Le zygote se scinde en deux, et cette distinction est la vraie conclusion.**
Le §8.5 énumère trois choses pré-chargées pendant l'idle. Le run se réduisant
ensuite à « un checkout du commit et un `exec` », elles ne survivent pas toutes :

| Pré-chargé | Survit à l'`exec` | Vaut |
|---|---|---|
| Collections installées sur disque | oui | non mesuré |
| Objets git du dépôt déjà tirés | oui | ~0,2 s |
| Interpréteur Python et Ansible importés | **non** | **~0,8 s** |

Un `exec` remplace le processus : ce qui était en mémoire disparaît. Les deux
premières lignes sont du pré-chargement **sur disque** — faciles, sans
mécanisme particulier, à faire dès le pool. La troisième exige de **forker** un
processus pré-initialisé plutôt que de l'`exec`er, et c'est elle seule qui
constitue le zygote au sens propre. C'est cette pièce-là qui est différée.

**Le pré-tirage de l'image fait partie du pré-chauffage.** Un pod chaud sur un
nœud dépourvu de l'image ne pré-chauffe rien : les 17,5 s de pull sont payées à
l'instant précis où le pool existait pour les éviter. L'ADR-0014, en confiant
l'image au projet, multiplie les images distinctes et rend ce point structurant
plutôt qu'accessoire.

**Critère de réouverture** : si le surcoût mesuré sur un playbook réel — avec
collections et collecte de faits — repasse au-dessus du budget malgré le pool,
alors les 795 ms redeviennent significatives et le zygote se réinstruit.

## Conséquences

- **Avantages** :
  - la question que la phase 0 devait trancher l'est, avec des chiffres
    reproductibles plutôt qu'une intuition ;
  - la pièce la plus complexe de la spec est écartée du chemin critique sans
    être abandonnée ;
  - les deux tiers faciles du §8.5 sont récupérés dès le pool, donc tôt ;
  - le pré-tirage d'image entre dans la conception du pool au lieu d'être
    découvert en exploitation.

- **Contraintes** :
  - **Un seul nœud mesuré.** `muzi` est le plus rapide des deux ordonnançables ;
    `tiny` est le nœud de contrôle et `kodi` est arm64 et fermé. Les chiffres
    sont un plancher, pas une moyenne de parc.
  - **Le poste « collections » est entièrement non mesuré.** L'image utilisée
    n'en embarque aucune. C'est d'autant plus gênant que l'ADR-0014 vient d'en
    faire le contenu principal des images projet : la ligne inconnue du tableau
    est désormais celle qui varie le plus d'un projet à l'autre.
  - **L'image mesurée n'est pas celle du produit.** `quay.io/ansible/ansible-runner`
    a servi de substitut ; l'image par défaut de l'ADR-0014 n'existe pas encore.
  - Les ~795 ms d'`ansible-playbook` sont un plancher qu'aucun pool ne supprime.
    Seul le zygote au sens propre les atteindrait.

- **Dette technique éventuelle** :
  - Refaire la mesure sur l'image par défaut réelle, avec collections, sur un
    playbook représentatif et contre des hôtes distants. C'est cette mesure-là,
    pas celle-ci, qui dira si la promesse du §3.1 tient en usage.
  - Le hit rate du pool conditionne tout : à 0 % de hit, on retombe sur les
    2,3 s. Le §8.3 prévoit déjà la métrique ; elle devient un indicateur de
    tenue de la promesse produit, pas seulement d'efficacité de ressources.

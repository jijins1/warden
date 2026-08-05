# ADR-0016 — Pas de verrou de concurrence, mais une visibilité des runs en vol

**Date** : 2026-08-05
**Statut** : Accepted

## Contexte

Le §14 posait la question sous forme de granularité : « verrou de concurrence :
inventaire ou hôte ? ». Elle présuppose qu'il en faut un.

L'inquiétude est réelle : deux playbooks lancés en même temps sur le même
inventaire peuvent se marcher dessus, et le second peut défaire ce que le
premier venait de faire. Le §6.6 décrit d'ailleurs le symptôme voisin — partir
déboguer un état partiel sans savoir qu'un run l'a laissé là.

Mais deux faits pèsent contre le verrou.

**Ansible n'en a pas.** Rien dans `ansible-playbook` n'empêche deux exécutions
simultanées sur le même inventaire. Le produit se présente comme un proxy dont
la commande est « quasi identique à `ansible-playbook` » (§5.1), et le §5.1 pose
que toute divergence doit être justifiée par une valeur ajoutée visible.

**Le bastion SSH non plus.** Le §5.7 conserve délibérément le bastion comme
porte de sortie. Un verrou côté proxy serait donc contournable par construction :
il n'empêcherait pas la collision, il déplacerait seulement l'utilisateur pressé
hors du périmètre audité — soit exactement l'inverse de l'effet recherché.

## Solutions envisagées

### Option A — Verrou exclusif par inventaire
Un seul run à la fois par inventaire ; les suivants sont mis en file.
- **Avantage** : supprime la classe entière des collisions à l'intérieur du
  produit ; simple à expliquer.
- **Rejetée parce que** : elle transforme le surcoût du produit en attente non
  bornée. Le §3.1 promet moins de 3 secondes ; un utilisateur qui attend la fin
  du playbook de vingt minutes d'un collègue ne perd pas 3 secondes, il perd
  vingt minutes. C'est la manière la plus brutale de casser la promesse
  centrale, et le contournement — le bastion — est à portée de main.

### Option B — Verrou par hôte
Granularité fine : deux runs coexistent tant que leurs `--limit` ne se
recoupent pas.
- **Avantage** : bien plus permissif que l'option A.
- **Rejetée parce que** : le périmètre réel d'un run n'est pas connu avant son
  exécution. `--limit` est un motif, les groupes sont dynamiques, et le §2 pose
  explicitement que `--limit` est un garde-fou contre l'erreur humaine, **jamais
  un contrôle de sécurité** — un playbook peut sortir de son périmètre annoncé.
  Verrouiller sur cette base donnerait une garantie fausse, ce qui est pire
  qu'aucune garantie.

### Option C — Aucun verrou, visibilité des runs en vol ✅ (retenue)
Rien n'est bloqué. Un run qui démarre alors qu'un autre est en cours sur le même
inventaire le signale à l'utilisateur, qui décide.
- **Choisie parce que** : elle ne rend le produit ni plus restrictif ni moins
  informatif que ce qu'il remplace, et elle est cohérente avec la posture du §2
  — « l'audit documente l'intention, il n'empêche rien ».

## Décision

**Aucun verrou de concurrence, à aucune granularité.** Deux runs simultanés sur
le même inventaire sont autorisés, comme ils le sont avec `ansible-playbook` et
comme ils le sont via le bastion.

**En contrepartie, l'information est donnée avant de partir.** Le §6.6 prévoit
déjà d'avertir quand le dernier run sur l'inventaire a été *interrompu* :

```
⚠ Le dernier run sur staging a été interrompu à la tâche 12
  par alice il y a 20 minutes (user_interrupt).
```

Le même mécanisme, la même requête et le même emplacement d'affichage couvrent
le cas des runs **en cours** :

```
⚠ alice exécute site.yml sur staging depuis 4 minutes (tâche 8/23).
```

C'est une extension du §6.6, pas un mécanisme nouveau : l'état des runs en vol
est déjà en base, puisque le statut y est écrit avant même la création du pod
(§6.4). Le coût est une requête ; le bénéfice est que la collision devient un
choix informé au lieu d'une surprise.

**Cet avertissement ne bloque pas et ne demande pas de confirmation.** Une
confirmation serait un verrou déguisé, avec la friction du verrou et sans sa
garantie.

## Conséquences

- **Avantages** :
  - la promesse de latence du §3.1 n'est jamais mise en défaut par l'attente
    d'un tiers ;
  - aucune incitation à contourner le produit par le bastion, donc l'audit reste
    complet — ce qui est la seule chose que le produit garantisse réellement ;
  - rien à concevoir côté verrou : ni file, ni bail, ni expiration, ni
    libération après crash, ni la classe de bugs qui va avec ;
  - le cas « run interrompu » du §6.6 et le cas « run en cours » partagent une
    seule implémentation.

- **Contraintes** :
  - **Les collisions restent possibles et se produiront.** Le produit les rend
    visibles et traçables, il ne les prévient pas. C'est à documenter clairement
    : un utilisateur qui croirait le proxy protecteur serait plus exposé qu'avec
    le bastion, parce qu'il baisserait sa garde.
  - L'avertissement dépend de la fraîcheur du statut en base. Un run dont le pod
    est mort sans que le reconciler soit passé apparaîtra encore « en cours » —
    le §8.7 et le §6.4 gouvernent déjà ce délai, mais il devient visible par
    l'utilisateur, donc son ordre de grandeur compte.

- **Dette technique éventuelle** :
  - Si l'usage montre que les collisions coûtent réellement cher sur certains
    environnements, le repli n'est pas le verrou global mais une **politique par
    environnement** — la production pouvant exiger la sérialisation là où le
    développement ne le demande pas. Le §3.2 prévoit déjà des contraintes
    optionnelles par environnement (`--check` obligatoire, approbation, fenêtre
    de maintenance) ; la sérialisation y trouverait sa place sans rien imposer
    ailleurs.

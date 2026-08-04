# ADR-0001 — Go pour la CLI et le serveur

**Date** : 2026-08-04
**Statut** : Accepted

## Contexte

Le produit se compose de trois éléments (`specs/initial.md` §1.2) : une CLI
distribuée aux ingénieurs sur leur poste, un control plane, et un runner qui
délègue l'exécution à `ansible-runner`. Aucun langage n'était fixé.

Les contraintes qui pèsent réellement sur le choix :

- La CLI remplace `ansible-playbook` et doit s'installer sans friction sur des
  postes hétérogènes → **binaire unique, sans runtime à installer**.
- Le §8 de la spec est un contrôleur Kubernetes à part entière : watch informer
  sur les pods, persistance des statuts terminaux, suppression explicite,
  reconciler de démarrage, claim atomique du pool de pods chauds.
- Le §9 impose un client de bus persistant (NATS JetStream ou Redis Streams) et
  un serveur WebSocket multi-réplica.
- Le §5.3 impose un scan de secrets côté serveur.
- Le moteur Ansible n'est *pas* réimplémenté (§1.3) : `ansible-runner` tourne
  dans un processus séparé, dans le runner. Le langage du produit n'a donc
  aucune raison d'être aligné sur celui d'Ansible.

## Solutions envisagées

### Option A — Rust
Écosystème git embarqué supérieur (gitoxide), CLI très performante, binaire
statique.
- **Avantage** : le meilleur moteur git en bibliothèque, utile vu l'ADR-0002.
- **Rejetée parce que** : le cœur du §8 est un contrôleur Kubernetes, et il n'y
  a pas d'équivalent mature à `client-go` et à son mécanisme d'informers. Le
  gain de gitoxide sur go-git ne compense pas le coût d'écrire un contrôleur
  Kubernetes sur des fondations moins établies.

### Option B — Python
Proximité culturelle avec Ansible.
- **Rejetée parce que** : la distribution de la CLI est le point dur, et Python
  n'offre pas de binaire unique sans empaquetage lourd. La proximité avec
  Ansible est par ailleurs illusoire : `ansible-runner` s'exécute dans un
  processus distinct, dans un autre conteneur.

### Option C — Go ✅ (retenue)
Un seul langage pour la CLI, le serveur et le contrôleur.
- **Choisie parce que** : binaire statique unique et cross-compilation triviale
  pour la CLI ; `client-go` et les informers sont l'implémentation de référence
  pour le §8 ; clients NATS et Redis matures ; scan de secrets disponible en
  bibliothèque ; un seul langage réduit le coût de partage du modèle de données
  entre CLI et serveur.

## Décision

Go pour la CLI, le serveur et le contrôleur Kubernetes. Le runner reste une
image contenant `ansible-runner` (Python), pilotée par un processus Go qui
consomme son flux d'events JSON.

## Conséquences

- **Avantages** : distribution de la CLI triviale ; accès direct à l'écosystème
  Kubernetes ; une seule chaîne de build.
- **Contraintes** : la bibliothèque git embarquée disponible en Go pur est
  go-git, sensiblement moins complète que gitoxide ou libgit2 — les limites qui
  en découlent sont documentées dans l'ADR-0002.
- **Dette technique éventuelle** : aucune bibliothèque Go pure ne couvre la
  maintenance des dépôts (repack, gc, commit-graph). Le mécanisme reste à
  trancher (cf. ADR-0002, section Dette).

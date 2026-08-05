# ADR-0009 — La configuration effective est résolue depuis le commit

**Date** : 2026-08-05
**Statut** : Accepted

## Contexte

`specs/initial.md` §5.4.2 tire la leçon d'Atlantis : le fichier du client est une
**requête**, le serveur détient la politique, et le résultat effectif est
journalisé. La spec s'arrête là, et laisse une question sans réponse : **de quelle
copie de `.ansibleproxy.yml` le serveur tire-t-il la requête ?**

La question n'est pas rhétorique. Le chemin naturel d'implémentation est le
suivant : la CLI lit le fichier sur le poste, en extrait `default_env`,
`inventory`, `forward_env`, et les place dans le corps du `POST /runs`. C'est ce
qui vient spontanément — le fichier est déjà lu localement pour connaître
l'adresse du serveur.

Ce chemin crée un écart silencieux. §5.4 affirme que le fichier est « commité
dans le repo, donc visible dans l'audit ». Mais si le serveur se sert des champs
recopiés dans la requête, ce qui est **audité** est le fichier contenu dans le
commit, tandis que ce qui **s'applique** est ce que la CLI a bien voulu envoyer.
Les deux peuvent diverger, et rien ne le montre. Une CLI modifiée — ou un simple
proxy HTTP — désigne un autre inventaire ou un autre environnement que celui que
l'auditeur lira dans le commit.

C'est exactement la vulnérabilité d'Atlantis, déplacée d'un cran : §5.4.2 ferme
la porte de la surcharge du comportement serveur, mais pas celle de la
divergence entre le fichier audité et le fichier appliqué.

Ce n'est visible qu'en mode DEV, où le contenu exécuté vient du poste. En mode
PROD, le SHA appartient à la branche protégée (§3.2), donc la configuration
aussi — à condition qu'elle soit bien lue depuis ce SHA.

## Solutions envisagées

### Option A — Le serveur exploite les champs résolus par la CLI
La CLI parse le fichier et transmet le résultat dans la requête.
- **Avantage** : aucun accès au dépôt nécessaire avant de démarrer un run.
- **Rejetée parce que** : elle place la résolution de la configuration dans un
  composant qui s'exécute sur le poste de l'utilisateur, donc entièrement sous
  son contrôle, et elle rend l'audit menteur — le commit journalisé ne décrit
  plus le run.

### Option C — Supprimer le fichier du dépôt, toute la configuration côté serveur
Environnement, inventaire et variables déclarés uniquement dans la politique
serveur.
- **Avantage** : aucune divergence possible, par construction.
- **Rejetée parce que** : elle détruit l'UX visée au §5.1 — configurer un projet
  deviendrait un ticket auprès de l'exploitant — et elle retire de l'audit
  l'information la plus utile en post-mortem : le diff de la configuration entre
  deux runs, que §6.5 compte précisément sur git pour donner.

### Option B — Le serveur lit le fichier dans l'arbre du commit ✅ (retenue)
La source unique de la requête est `<sha>:.ansibleproxy.yml`, extrait du commit
reçu.
- **Choisie parce que** : ce qui s'applique et ce qui est audité deviennent le
  **même octet**. La propriété tient sans discipline côté client, et sans rien
  retirer au modèle du §5.4.

## Décision

La configuration effective est résolue **exclusivement** à partir du fichier
contenu dans le commit du run, lu par le rôle `git` (ADR-0006) avec
`git cat-file blob <sha>:.ansibleproxy.yml`.

**Aucun champ de configuration ne transite par la requête.** `POST /runs` ne
porte que :

- le SHA du run et le mode (DEV / PROD) ;
- les **flags explicites** tapés par l'utilisateur (`--limit`, `--tags`, `--check`,
  `-e`…), qui sont par nature une intention du moment et non de la configuration
  de projet ;
- les **valeurs** des variables d'environnement à transmettre — elles ne peuvent
  venir que du shell de l'appelant (§5.4).

**Une exception, structurelle : le champ `server`.** La CLI doit bien lire
localement l'adresse du serveur, puisqu'il faut joindre quelqu'un avant qu'un
commit existe de son côté. C'est le seul champ dont la lecture est
nécessairement cliente. La CLI peut lire les autres pour l'affichage et les
valeurs par défaut, mais **rien de ce qu'elle en déduit n'est autoritaire**.

**Composition de la configuration effective**, dans cet ordre :

1. politique serveur de l'environnement (`allowed_overrides`, allowlists — §5.4.1,
   §5.5) ;
2. fichier `.ansibleproxy.yml` **du commit**, filtré par 1 ;
3. flags explicites de la requête, filtrés par 1.

Cas particulier de `forward_env` : les **noms** viennent du commit et sont
intersectés avec l'allowlist serveur de l'environnement ; les **valeurs** viennent
du client et ne sont **jamais journalisées** (décision produit #5).

**Le résultat effectif est journalisé** (§5.4.2), à côté du SHA — et non ce que le
client a demandé.

**Fichier absent ou illisible dans le commit → run refusé.** L'ADR-0003 exigeait
déjà au moins un commit pour porter la configuration ; c'est ici que cette
exigence produit son effet.

**Le YAML du commit est une entrée non fiable** et se traite comme telle : taille
plafonnée, parseur strict, champs inconnus refusés plutôt qu'ignorés, alias et
ancres YAML désactivés.

## Conséquences

- **Avantages** :
  - le commit journalisé décrit intégralement le run, configuration comprise ;
  - le diff de configuration entre deux runs devient exploitable en post-mortem,
    au même titre que le diff de code (§6.5) ;
  - la propriété ne dépend d'aucune discipline côté CLI : une CLI modifiée ne
    peut pas faire diverger l'exécution de l'audit.

- **Contraintes** :
  - Le serveur doit lire un blob dans le dépôt **avant** de démarrer le run : un
    aller-retour `api` → `git` supplémentaire dans le chemin chaud. Peu coûteux,
    mais à compter dans le budget de 3 s (§3.1).
  - Refuser les champs inconnus fait échouer les runs d'un dépôt dont le fichier
    utilise une clé plus récente que le serveur. C'est le comportement voulu —
    ignorer silencieusement une clé de configuration est précisément le mode de
    panne qu'on cherche à éliminer — mais le message d'erreur doit nommer la clé
    et la version attendue (§5.7).

- **Dette technique éventuelle** :
  - Le format de la politique serveur par environnement — la couche 1 de la
    composition — n'est pas spécifié. Il relève de la phase 5 (§12) ; d'ici là, la
    couche existe dans le code avec une politique permissive, pour que l'ordre de
    composition soit en place et n'ait pas à être rétro-ajouté.
  - Le versionnage du schéma de `.ansibleproxy.yml` n'est pas traité : il faudra
    un champ de version et une politique de compatibilité avant que des dépôts
    tiers ne dépendent du format.

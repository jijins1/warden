# ADR-0017 — Redaction des `--diff` par substitution exacte des valeurs injectées

**Date** : 2026-08-05
**Statut** : Accepted

## Contexte

Le §6.5 qualifie le contenu de `--diff` de « donnée la plus précieuse de l'audit
et la plus dangereuse ». Précieuse parce qu'elle seule répond à « qu'est-ce qui
a *réellement* changé sur la machine », au caractère près — tout le reste du
journal dit ce qui était demandé. Dangereuse parce qu'un `template` rendant
`password: {{ vault_db_password }}` produit un diff contenant la valeur en clair.

Deux décisions antérieures cadrent le problème.

L'ADR-0008 a placé ces payloads dans une **table séparée avec un contrôle
d'accès distinct** : ouvrir le journal ne donne pas accès aux diffs. C'est une
barrière, ce n'est pas la seule nécessaire.

L'ADR-0013 a supprimé le filet temporel. Tant qu'une purge existait, une
redaction imparfaite avait une date de péremption. Sans purge, **ce qui entre
dans cette table y reste**. La redaction à l'écriture devient donc le contrôle
porteur, et non plus un contrôle d'appoint.

## Solutions envisagées

### Option A — Détection heuristique des secrets
Entropie, motifs connus (clés AWS, clés privées, jetons), noms de variables
suspects.
- **Avantage** : ne suppose rien sur l'origine des valeurs ; c'est ce que font
  les scanners de secrets sur du code source.
- **Rejetée parce que** : il n'y a pas de motif. `password: hunter2` dans un
  fichier de configuration ressemble à n'importe quelle chaîne dans n'importe
  quel fichier. Une heuristique produit des faux négatifs, et depuis l'ADR-0013
  un faux négatif est définitif.

### Option B — Aucune redaction, s'en remettre au contrôle d'accès
La table séparée de l'ADR-0008 suffit.
- **Rejetée parce que** : elle fait reposer la protection de la donnée la plus
  sensible du produit sur une seule ligne de configuration d'accès. Une erreur
  de `GRANT`, une sauvegarde mal rangée, un accès de dépannage, et l'exposition
  est totale et rétroactive sur tout l'historique.

### Option C — Chiffrement avec des clés que l'exploitant ne détient pas
- **Rejetée parce que** : la gestion de clés devient un composant du produit à
  part entière, et un auditeur qui ne peut pas lire le diff perd exactement la
  valeur pour laquelle le §6.5 le capture. Cela ne dispense d'ailleurs pas de
  savoir ce qui est un secret : c'est une protection *en plus*, pas *à la place*.

### Option D — Substitution exacte des valeurs injectées et de leurs dérivées ✅ (retenue)
- **Choisie parce que** : **le produit connaît les valeurs**. C'est lui qui les
  injecte, au moment du claim (§8.3), depuis un lease Vault (§7). Il n'a donc
  pas à *deviner* ce qui est un secret : il cherche ce qu'il a lui-même fourni.
  Correspondance exacte, aucun faux positif.

## Décision

**Substitution exacte des valeurs injectées et de leurs dérivations
calculables**, dans le runner, avant transmission.

### Jeu de candidats

Pour chaque secret injecté, on précalcule :

| Dérivation | Note |
|---|---|
| la valeur brute | |
| `b64encode`, **trois alignements** | base64 encode par blocs de 3 octets : le secret encodé *à l'intérieur* d'une structure plus large a un décalage. Il n'existe que trois alignements possibles, on cherche la sous-chaîne centrale de chacun |
| `urlencode` | |
| échappement `to_json` | |
| `quote` (shell) | |
| `hash('md5'\|'sha1'\|'sha256'\|'sha512')` — **non salés** | rainbow-tables : à traiter comme la valeur elle-même |
| `upper` / `lower` | |

### Ce qui n'est pas couvert, et pourquoi

**Les hachages salés ne sont pas précalculables.** `{{ password |
password_hash('sha512') }}` produit un crypt avec un sel différent à chaque
appel. C'est pourtant le cas le plus fréquent en pratique — c'est ainsi qu'on
positionne un mot de passe utilisateur via le module `user`, donc dans
`/etc/shadow`.

C'est assumé, et la gravité n'est pas la même : un crypt salé n'est pas du
plaintext, c'est le contenu que le playbook a précisément pour mission d'écrire.
Il reste cassable hors ligne si le secret est faible — ce qui est un problème de
qualité du secret, pas de redaction.

Ne sont pas couverts non plus le chiffrement et toute dérivation comportant de
l'aléa par exécution. Par construction.

### Où, et selon quel mode de défaillance

**Dans le runner, jamais dans le serveur.** Si le pod transmettait le diff brut
pour que le serveur le masque, la valeur en clair aurait déjà traversé le
réseau et pourrait subsister dans un buffer, un log de proxy ou un vidage
mémoire. Le runner, lui, connaît déjà les secrets — il en a besoin pour exécuter
le playbook, et l'ADR-0011 pose qu'il est hostile et lisible de toute façon. Y
placer la redaction n'ajoute aucune exposition et garantit que la valeur en clair
**ne sort jamais du pod**.

**Fail-closed par défaut** : si le jeu de secrets n'est pas disponible ou si le
redacteur échoue, le payload est abandonné, jamais transmis en clair.

**Le marqueur nomme la variable** : `[REDACTED: vault_db_password]`. L'auditeur
sait qu'un secret occupait cette place et lequel, sans en connaître la valeur.

### Deux exceptions délibérées, en fail-open

Elles sont des **décisions**, pas des lacunes, et le produit les signale plutôt
que de les taire.

**Secret de moins de 5 caractères : aucune substitution tentée.** Un secret de
quatre caractères est déjà cassé ; le masquer ne protégerait rien et rendrait
illisibles tous les diffs du parc, la chaîne apparaissant partout. Le run porte
un avertissement nommant la variable.

**Au-delà d'un seuil de correspondances : le payload est stocké tel quel.** Le
run porte un avertissement. Le cas visé est celui d'un secret écrit dans un
grand nombre de lignes générées, où une substitution intégrale ne laisserait
rien d'exploitable.

C'est le point le plus coûteux de cette décision et il doit être lu sans
atténuation : **un secret long et valide peut être stocké en clair, et
l'ADR-0013 ayant supprimé toute purge, « stocké » signifie définitivement.** Le
choix retenu privilégie la valeur d'audit du diff sur la réduction de cette
exposition ; l'alternative — abandonner le payload — a été examinée et écartée.

Le seuil est un paramètre de politique serveur, pas une constante du code.

## Conséquences

- **Avantages** :
  - aucun faux positif : on ne cherche que ce qu'on a soi-même injecté ;
  - la valeur en clair d'un secret ne quitte jamais le pod ;
  - le cas courant — un secret rendu tel quel, encodé en base64 ou haché sans
    sel dans un fichier de configuration — est couvert exactement, pas
    heuristiquement ;
  - le marqueur conserve l'information d'audit (« un secret était là, celui-ci »)
    que la simple suppression détruirait.

- **Contraintes** :
  - **Les deux exceptions sont des expositions réelles et permanentes**, pas des
    approximations. Elles doivent apparaître dans l'interface d'audit du §6.5,
    au niveau du run, et pas seulement dans un journal technique.
  - Le seuil de correspondances n'a aucune valeur mesurée. Choisi trop bas, il
    laisse passer des diffs qu'on aurait pu masquer ; trop haut, il caviarde des
    diffs sans intérêt résiduel.
  - La redaction impose au runner de détenir le jeu complet des secrets sous
    forme exploitable pendant tout le run, y compris après que le playbook n'en
    a plus besoin.
  - Les hachages salés restent hors de portée, et ils sont fréquents.

- **Dette technique éventuelle** :
  - **La vraie correction de la première exception n'est pas dans le redacteur,
    elle est dans la politique** (§10) : refuser les secrets de moins de 5
    caractères à l'injection ferait disparaître le cas. Tant que ce n'est pas
    fait, le produit accepte un secret qu'il sait ne pas pouvoir protéger.
  - Une agrégation est nécessaire : « N runs contiennent des secrets non
    masqués » est la question que se posera un RSSI, et elle ne doit pas exiger
    de parcourir les runs un par un.
  - Aucune procédure n'existe pour purger *après coup* un secret exposé par ces
    exceptions, l'ADR-0013 ayant écarté tout mécanisme de purge. Une rotation du
    secret concerné reste la seule réponse, et elle doit être documentée comme
    telle.

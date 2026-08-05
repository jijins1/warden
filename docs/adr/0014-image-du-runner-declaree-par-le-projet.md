# ADR-0014 — L'image du runner est déclarée par le projet

**Date** : 2026-08-05
**Statut** : Accepted

## Contexte

Il faut décider comment les collections Ansible d'un projet arrivent dans le pod
runner. La question paraît secondaire ; elle ne l'est pas, parce qu'elle touche
trois contraintes déjà posées.

**Le §5.3 signale la limite en toutes lettres** : « les collections/rôles Galaxy
ne sont pas dans le diff. Sans miroir interne avec versions figées, le run n'est
pas rejouable à l'identique. » Le §14 laisse la stratégie de miroir ouverte.

**Le pod runner n'a pas d'egress libre.** Le §7 et l'ADR-0007 restreignent ses
sorties réseau par NetworkPolicy, parce qu'il exécute du code arbitraire (§2).
Un `ansible-galaxy install` au démarrage du run exige d'ouvrir un trou vers
Galaxy ou vers un miroir — dans le composant dont c'est précisément la mission
de n'en avoir aucun.

**Les collections ne peuvent pas être cuites dans une image partagée.** Chaque
dépôt a son `requirements.yml` : une image unique pour tous les projets ne peut
pas les contenir tous.

Enfin, la mesure de la phase 0 déplace l'ordre des priorités. Sur un nœud chaud,
la création du pod coûte ~1,3 s et le démarrage d'`ansible-playbook` ~0,8 s.
Mais **le pull d'une image de 320 Mo sur un nœud froid coûte ~17,5 s** : c'est
le terme dominant, d'un ordre de grandeur. Toute solution qui multiplie les
images distinctes paie sur ce poste.

## Solutions envisagées

### Option A — Installation depuis Galaxy ou un miroir au démarrage du run
Le pod lit `requirements.yml` et lance `ansible-galaxy install`.
- **Avantage** : aucune contrainte sur le dépôt de l'utilisateur ni sur l'image ;
  c'est le fonctionnement habituel d'Ansible, donc rien à expliquer.
- **Rejetée parce que** : il faut ouvrir un egress depuis le pod hostile, ce que
  tout le §7 s'emploie à fermer ; le coût réseau est payé à chaque run ; et la
  reproductibilité reste suspendue à la disponibilité et à l'immuabilité d'un
  service tiers, ce qui est exactement la limite que le §5.3 dénonce.

### Option B — Collections vendorisées dans le dépôt de l'utilisateur
Le code des collections est commité sous `collections/ansible_collections/`, et
voyage avec le transfert git.
- **Avantage** : aucun egress ; déduplication par blob (§6.2) ; le commit
  d'audit contient le code réellement exécuté.
- **Rejetée parce que** : elle impose une modification à tous les dépôts des
  utilisateurs, c'est-à-dire un changement de leurs habitudes pour un besoin qui
  est le nôtre. Et elle n'épingle que les collections : `ansible-core`,
  l'interpréteur Python et les bibliothèques système restent ceux de l'image,
  donc la reproductibilité reste partielle.

### Option C — L'image du runner est déclarée dans la config du projet ✅ (retenue)
`.ansibleproxy.yml` porte un champ `runner_image`. Le projet qui a besoin de
collections particulières publie une image qui les contient.
- **Choisie parce que** : elle ne demande rien aux 90 % de projets qui se
  contentent de l'image par défaut ; elle n'exige aucun egress du pod ; et elle
  épingle **plus** que l'option B — collections, `ansible-core`, Python et
  système d'un seul coup.

## Décision

**`runner_image` est un champ de `.ansibleproxy.yml`**, donc résolu depuis
l'arbre du commit (ADR-0009). Il est par conséquent versionné avec le code, revu
dans la même modification, et inscrit dans l'artefact d'audit sans effort
supplémentaire.

**Ce champ ne peut jamais provenir d'un flag de la CLI.** L'ADR-0009 a fermé
précisément cette faille : une image choisie côté client ne serait pas celle que
l'audit décrit.

**Le produit publie une image par défaut** pour le cas courant — `ansible-core`,
`community.general`, `ansible.posix`. Un projet qui ne déclare rien l'utilise.

**La précision de la référence appartient au projet, y compris en PROD.** Un tag
mouvant, un tag de version ou un digest sont tous acceptés. Un tag mouvant
affaiblit la garantie du §3.2 — le contenu peut changer sans commit — mais c'est
un risque que le projet prend pour lui-même, et le §2 pose déjà que « l'audit
documente l'intention, il n'empêche rien ». Imposer le digest serait un contrôle
que le produit n'applique nulle part ailleurs.

**Contrepartie, qui ne coûte qu'un champ : le digest résolu est enregistré au
journal.** Kubernetes l'expose dans `status.containerStatuses[].imageID`, même
lorsque le projet a écrit `:latest`. Le produit n'interdit rien et reste capable
de répondre à « quelle image a réellement tourné ce jour-là », ce qui est sa
fonction.

**L'agent est injecté, l'image utilisateur n'a aucun contrat à respecter.** Un
initContainer dépose le binaire `warden-runner` sur un `emptyDir` partagé. Deux
raisons de préférer ça à une image utilisateur construite `FROM` une base à
nous :

- l'équipe projet n'est pas couplée à notre cycle de publication ;
- l'agent reste dans **notre** image, donc dans notre chaîne de suivi CVE et pas
  dans la leur.

Le binaire est statique (`CGO_ENABLED=0`), il fonctionne donc quelle que soit la
distribution de base choisie par le projet.

**Le pré-tirage d'image devient une pièce du pré-chauffage** (§8.3), pas une
optimisation ultérieure. Un pod chaud sur un nœud qui n'a pas l'image ne
pré-chauffe rien : les 17,5 s mesurées sont payées au moment précis où le pool
existait pour les éviter.

## Conséquences

- **Avantages** :
  - la question §14 sur le miroir Galaxy se referme : il n'y en a plus besoin ;
  - la limite de rejouabilité du §5.3 disparaît dès qu'un projet épingle son
    image, et il peut le décider seul ;
  - aucun egress supplémentaire depuis le pod hostile ;
  - le choix de l'image est audité par construction, puisqu'il vit dans le
    commit.

- **Contraintes** :
  - **N projets, N images à tirer.** C'est le poste dominant de la latence, et
    l'option B ne le payait pas. La clé de pool contenant déjà le dépôt, la même
    image retombe sur les mêmes nœuds, mais le pré-tirage doit être conçu, pas
    espéré.
  - **L'image par défaut devient un livrable du produit**, avec sa surface CVE et
    son rythme de mise à jour.
  - **Le pré-chauffage doit deviner l'image.** Elle vient de la config du commit,
    or le pool démarre avant le run. Le pré-chauffage se fonde donc sur le
    dernier commit connu ; une image différente au moment du run rend le pod
    chaud inutilisable. La dégradation doit être explicitement codée — retomber
    en démarrage à froid — et non subie.
  - Un projet qui déclare une image que le cluster ne peut pas tirer obtient un
    `ImagePullBackOff`, dont le message doit être remonté tel quel à
    l'utilisateur. Une erreur non actionnable renverrait l'ingénieur au bastion
    SSH (§5.7).

- **Dette technique éventuelle** :
  - **Aucune allowlist de registres.** Un projet peut donc faire tirer au cluster
    une image de n'importe quelle origine et de n'importe quelle taille. À noter
    que l'egress concerné est celui du **cluster**, pas celui du pod runner : il
    échappe aux NetworkPolicy du §7, qui ne gouvernent pas le kubelet. Différé
    consciemment ; à traiter avec la politique serveur de l'ADR-0009, dont
    `allowed_overrides` est le point d'accroche naturel.
  - Rien n'impose que l'image contienne effectivement `ansible-runner`. Une image
    invalide échoue au démarrage du run, tard et probablement mal. Une
    vérification au premier usage d'une image donnée, avec un message clair,
    reste à concevoir.
  - Le coût de chargement des collections n'a pas été mesuré, ni à l'installation
    ni au chargement du play. La phase 0 s'est déroulée sur une image qui n'en
    contient aucune.

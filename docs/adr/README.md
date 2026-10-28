# Architecture Decision Records

Journal des décisions d'architecture du projet. Une décision structurante se
documente ici **au moment où elle est prise**, avec les options rejetées et
leur raison de rejet — c'est ce qui évite de re-débattre le même point six mois
plus tard.

Le document de conception produit (`specs/initial.md`) décrit *quoi* construire
et récapitule les décisions produit (§13). Les ADR documentent les décisions
d'implémentation prises en cours de route.

## Index

| ADR | Titre | Statut |
|---|---|---|
| [0001](0001-go-pour-cli-et-serveur.md) | Go pour la CLI et le serveur | Accepted |
| [0002](0002-go-git-plutot-que-binaire-git.md) | go-git plutôt que le binaire git système | Superseded par 0005 |
| [0003](0003-commit-audit-chaine-sur-la-base.md) | Commit d'audit chaîné sur le commit de base | Accepted |
| [0004](0004-maintenance-des-depots-par-le-binaire-git.md) | Maintenance des dépôts serveur par le binaire git | Accepted (amendé par 0005) |
| [0005](0005-binaire-git-comme-moteur-unique.md) | Le binaire git comme moteur unique | Accepted |
| [0006](0006-un-binaire-trois-roles.md) | Un binaire, trois rôles déployés séparément | Accepted |
| [0007](0007-acces-du-runner-au-code.md) | Accès du runner au code par fetch scopé à un SHA | Accepted (amendée le 2026-08-13) |
| [0008](0008-postgresql-comme-journal-et-transport-devents.md) | PostgreSQL comme journal et comme transport des events | Accepted |
| [0009](0009-configuration-resolue-depuis-le-commit.md) | La configuration effective est résolue depuis le commit | Accepted |
| [0010](0010-strategie-de-test-en-trois-etages.md) | Stratégie de test en trois étages, sans cluster jetable | Accepted |
| [0011](0011-un-binaire-par-domaine-de-confiance.md) | Un binaire par domaine de confiance, frontières imposées par le compilateur | Accepted |
| [0012](0012-migrations-sql-avec-goose.md) | Migrations SQL embarquées avec goose, appliquées par un Job dédié | Accepted |
| [0013](0013-retention-differee-des-refs-daudit.md) | Aucune purge avant mesure : la rétention des refs d'audit est différée | Accepted |
| [0014](0014-image-du-runner-declaree-par-le-projet.md) | L'image du runner est déclarée par le projet | Accepted |
| [0015](0015-prechauffage-obligatoire-zygote-optionnel.md) | Le pré-chauffage est obligatoire, le zygote reste optionnel | Accepted |
| [0016](0016-pas-de-verrou-de-concurrence.md) | Pas de verrou de concurrence, mais une visibilité des runs en vol | Accepted |
| [0017](0017-redaction-des-diffs-par-substitution-exacte.md) | Redaction des `--diff` par substitution exacte des valeurs injectées | Accepted |
| [0018](0018-canal-de-dispatch-sse-vers-le-runner.md) | Canal de dispatch SSE du contrôle vers le runner | Accepted (amendé le 2026-08-06) |
| [0019](0019-decoupage-hexagonal-du-control-plane.md) | Découpage hexagonal du control plane | Accepted |
| [0020](0020-key-inventory-credentials-dacces.md) | `key-inventory` : credentials d'accès, Secrets Kubernetes en v1 | Accepted |
| [0021](0021-modele-devent-normalise.md) | Modèle d'event normalisé, identifiants et métadonnées de run | Accepted (amendé le 2026-08-06) |
| [0022](0022-pgx-comme-pilote-postgresql.md) | pgx en accès natif, `database/sql` réservé aux migrations | Accepted |
| [0023](0023-identite-damorcage-du-pod-runner.md) | Identité d'amorçage du pod runner : jeton porteur émis par le contrôleur | Accepted |
| [0024](0024-api-interne-du-role-git.md) | API interne du rôle `git` : HTTP sur port distinct, jeton porteur partagé | Accepted |
| [0025](0025-perimetre-de-la-capture-daudit.md) | Périmètre de la capture d'audit : l'arbre dit ce qui va tourner | Accepted (amendée le 2026-08-14) |
| [0026](0026-isolation-de-lecture-du-role-git.md) | L'isolation de lecture du rôle `git` est une conjonction, pas un espace de noms | Accepted (amendée le 2026-08-14) |
| [0027](0027-ecriture-du-role-git.md) | Écriture du rôle `git` : quarantaine native, fraîcheur des objets, ref d'audit écrite une fois | Accepted |
| [0028](0028-jeton-de-push-auto-porteur.md) | Les jetons git portent leur propre portée signée ; le serveur écrit le miroir de la principale | Accepted |
| [0029](0029-remontee-devents-du-runner.md) | Remontée d'events du runner : un seul message montant, qui clôt aussi le run | Accepted |
| [0030](0030-enrolement-des-machines.md) | Enrôlement des machines : deux modes, deux magasins, et l'egress qu'on a ouvert | Accepted |
| [0031](0031-reconciliation-des-runs-tues.md) | Réconcilier les runs tués : qui a le droit de prononcer une issue | Accepted |
| [0032](0032-comptes-locaux-et-sessions.md) | Comptes locaux et sessions : une identité vérifiée, en attendant OIDC | Accepted |
| [0033](0033-droits-par-environnement.md) | Droits par environnement : la moitié de l'autorisation qui ne dépend pas du réseau | Accepted |
| [0034](0034-schema-du-fichier-de-projet.md) | Schéma de `.ansibleproxy.yml` : substitution fermée, noms réservés, et ce que l'allowlist protège vraiment | Accepted |
| [0035](0035-surface-navigateur.md) | Surface navigateur : une origine déclarée, un cookie, et UNE seule allowlist | Accepted |
| [0036](0036-adresse-ou-nom-resoluble.md) | L'adresse d'une machine accepte un nom, et le risque change de main | Accepted |
| [0037](0037-enrolement-depuis-linventaire-ansible.md) | Enrôler depuis l'inventaire Ansible, et ne pas devenir Ansible | Accepted |
| [0038](0038-flags-ansible-check-diff-limit.md) | Trois flags Ansible, une allowlist, et le maillon qui manquait | Accepted |
| [0039](0039-extra-vars-declarees-par-le-commit.md) | `-e` : les noms viennent du commit, les valeurs sont auditées | Accepted |
| [0040](0040-submit-suit-le-run.md) | `warden submit` suit le run, et son code de sortie le dit | Accepted |
| [0041](0041-adhoc-comme-genre-de-run.md) | L'ad-hoc comme troisième genre de run, sans allowlist de modules | Accepted |
| [0042](0042-commit-daudit-frappe-par-le-serveur.md) | Le commit d'audit d'un run lancé depuis le navigateur est frappé par le serveur | Accepted |
| [0043](0043-diagnostic-dun-run-sans-tache.md) | Dire pourquoi un run n'a rien exécuté | Accepted |
| [0044](0044-environnements-proteges.md) | Options par environnement : un avertissement déclaré, et un gel indépendant des droits | Accepted |
| [0045](0045-vault-inventory-cle-ansible-vault.md) | `vault-inventory` : la clé Ansible Vault, une par environnement | Accepted |
| [0046](0046-la-sortie-de-la-cli-est-celle-dansible.md) | La sortie de la CLI est celle d'Ansible, couleurs comprises | Accepted |
| [0047](0047-runners-docker-et-secrets-dans-vault.md) | Runners lancés par l'API Docker, secrets dans Vault | Proposed |
| [0048](0048-connexion-par-mot-de-passe-sans-enrolement.md) | Connexion par mot de passe, sans enrôlement, empreintes vérifiées | Proposed |
| [0049](0049-mise-a-jour-de-la-cli-alignee-sur-le-serveur.md) | Mise à jour de la CLI alignée sur le serveur, binaires signés par minisign | Proposed |

## Convention

- Numérotation séquentielle : `NNNN-titre-court.md`
- Langue : français
- Statut : `Accepted` pour toute décision en vigueur
- Au moins deux options envisagées, avec la raison de rejet explicite

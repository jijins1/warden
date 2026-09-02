// Package ansible lit un inventaire Ansible pour en tirer les machines à
// enrôler.
//
// # Ce n'est pas un moteur d'inventaire
//
// C'est le point à garder en tête. Ansible résout des variables par une
// hiérarchie de sources — `group_vars/`, `host_vars/`, les plugins d'inventaire,
// les `--extra-vars` — dont la précédence tient sur une page de documentation.
// Ce paquet n'en implémente rien : il extrait TROIS faits de connexion —
// `ansible_host`, `ansible_user`, `ansible_port` — d'un document YAML, et ne lit
// que les `vars:` écrits dans ce document.
//
// La conséquence à connaître : un `ansible_user` posé dans `group_vars/all.yml`
// est INVISIBLE ici. Ce n'est pas un oubli mais une frontière — la suivre
// jusqu'au bout demanderait de reproduire la précédence d'Ansible, et une
// reproduction approximative de cette précédence enrôlerait des machines sous le
// mauvais compte sans que rien ne le dise.
//
// # Ce que ce paquet ne décide pas non plus
//
// Rien de ce qu'il lit ne fait autorité au-delà de l'enrôlement. Ce qui décide
// de ce qui s'EXÉCUTE vient du commit d'audit (ADR-0009) ; cette lecture-ci sert
// à une commande d'administration, qui compare une liste de machines à
// l'inventaire du serveur.
package ansible

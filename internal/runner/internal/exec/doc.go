// Package exec pilote `ansible-runner` et lit son flux d'events.
//
// Réutiliser ansible-runner est une décision produit (#16) : le produit, c'est
// le transport git, la politique et l'UX de la CLI — pas un réimplémentation
// d'Ansible.
//
// Les prompts interactifs sont interdits en mode serveur (décision #15).
package exec

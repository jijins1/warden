// Package fetch matérialise l'arbre du commit à exécuter, par `git fetch` vers
// le rôle `git` — jamais vers le rôle `api` (ADR-0007).
//
// La portée est réduite structurellement : l'`upload-pack` est servi avec
// GIT_NAMESPACE, le runner ne voit donc qu'une seule ref, celle de son run.
// `--depth=1` est une optimisation de bande passante, JAMAIS un contrôle de
// sécurité.
package fetch

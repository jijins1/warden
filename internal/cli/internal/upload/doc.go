// Package upload construit ce qui est envoyé au serveur : périmètre de l'upload
// (§5.3), `.auditignore`, et commit d'audit chaîné sur le commit de base
// (ADR-0003).
//
// Contrainte non négociable (décision produit #4) : le worktree de
// l'utilisateur n'est JAMAIS modifié. Le commit se construit par
// `GIT_INDEX_FILE` et `commit-tree`, sans index ni HEAD du dépôt local.
package upload

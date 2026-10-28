---
name: convention-droits-et-secrets
description: Où les droits sont vérifiés et par quel chemin passent les secrets de passage dans warden (pour la revue sécurité)
metadata:
  type: project
---

- Droit par rôle : `internal/server/internal/inbound/api/server.go` (routes, `adminOuRien`) et `auth.go` (`Policy`).
- Droit par environnement : `autoriserLEnvironnement` dans l'API à la création, puis `core.VerifierLAcces` (`core/grants.go`) à la livraison de l'ordre. `VerifierLAcces` laisse passer un environnement VIDE : un run sans environnement est légitime.
- Les mots de passe humains (become, connexion) passent par la consigne `core.SecretStash`, lus et détruits à `OrderFor` (`core/order.go`), jamais en base.
- `Store.ListHosts(ctx, "")` rend les machines de TOUS les environnements (clause `$1 = ''`) : tout appelant doit s'assurer que l'environnement n'est pas vide.
- Logger : `log/slog`, on logge `run_ref` et `error`, pas les valeurs.

---
name: convention-core-go
description: Où se trouvent le domain et le port in dans warden (Go) — pas de package port/in, tout est dans internal/server/internal/core
metadata:
  type: project
---

Warden est en Go et n'a pas l'arborescence `domain` / `port/in` / `port/out` des skills. Le domaine du
control plane est `internal/server/internal/core` : modèle, politique de composition ET ports y
vivent ensemble (attesté par `internal/server/internal/core/doc.go` et l'en-tête de
`internal/server/internal/core/ports.go`).

- Les interfaces de `core/ports.go` (RunReader, EventFollower, RepoDiffer…) sont « les ports que les
  adaptateurs entrants consomment » : `inbound/api` les appelle, `outbound/store` les satisfait
  directement, sans service entre les deux. Elles jouent le rôle de port in pour ce profil.
- La règle « aucune infrastructure dans core » est vérifiée par le CI (ADR-0011, ADR-0019).
- `core` importe déjà `internal/wire/dispatch` (order.go) : c'est l'existant, pas un écart à relever.
- Des étiquettes json/yaml existent déjà dans core sur des types de configuration lue depuis des
  fichiers (protected.go, grants_plan.go, flags.go, hosts.go) : existant, ne pas réclamer la
  migration ; seule une étiquette ajoutée par le changement se relève.
- La `Policy` d'autorisation (CanViewRun…) vit dans `inbound/api/auth.go`, pas dans core : hors
  périmètre de ce profil. Exception : la visibilité de la liste des runs est passée dans core
  (`core.RunsVisibles`, `core/runs_visibles.go`), sous la forme d'une fonction qui reçoit ses ports
  en arguments.

**How to apply:** relire `core` comme le `domain`, et les interfaces/fonctions exportées de `core`
appelées par `inbound/` comme le `port/in`.

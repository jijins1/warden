---
name: decision-tags-json-listes-runs
description: Point écarté le 2026-10-27 — étiquettes json sur les types de core servis tels quels par GET /runs et GET /hosts/{host}/runs (RunListItem, RunListResult, PlaybookSummary)
metadata:
  type: feedback
---

Ne pas relever une étiquette json sur `core.RunListItem`, `core.RunListResult` ou `core.PlaybookSummary`
(par exemple `Auteur Auteur \`json:"Author"\`` dans `internal/server/internal/core/audit.go`).

Écarté par le développeur le 2026-10-27, en revue de la branche docs/adr-0047-docker-vault.

**Why:** dans ce projet, `GET /runs` et `GET /hosts/{host}/runs` sérialisent ces types de `core` tels
quels, sans DTO (`json.NewEncoder(w).Encode(result)` dans `inbound/api/runs_handler.go`). Retirer
l'étiquette imposerait un DTO miroir de 14 champs qui dériverait.

**How to apply:** ne pas reproposer, sauf si ces handlers se dotent de leur propre DTO, ou si les types
ne sont plus servis tels quels. Dans ce cas, citer cette décision en relevant le point. Voir aussi
[[convention-core-go]].

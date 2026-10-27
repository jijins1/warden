---
name: decision-docker-observer-integration
description: Écarté par le développeur — ne pas reproposer d'assertion sur le filtre d'ObserverPods / la borne de durée dans l'intégration Docker réelle
metadata:
  type: project
---

Point écarté (branche docs/adr-0047-docker-vault, 2026-10-27) : le filtre par label d'`ObserverPods` et la borne de durée ne sont testés que contre le faux démon (`internal/server/internal/outbound/docker/faux_test.go`), pas contre le démon réel (`docker_integration_test.go`).

**Why:** le faux démon couvre la logique du filtre, et ce manque ne fait pas courir de risque de sécurité (raison donnée par le développeur).

**How to apply:** ne pas relever à nouveau, sauf si le filtre ou la borne changent au point que cette raison ne tient plus (par exemple si le filtre devient une frontière de sécurité) ; citer alors cette décision.

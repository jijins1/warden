# Version minimale de git exigée par le produit (ADR-0005 : le binaire git est
# un composant, pas un outil). Doit rester alignée avec GIT_MIN_VERSION du
# Dockerfile du devcontainer et avec l'image du rôle `git`.
GIT_MIN_VERSION := 2.45.0

.PHONY: doctor
doctor: ## Vérifie que l'environnement de développement est complet
	@fail=0; \
	printf '%-18s' 'go'; \
	  if command -v go >/dev/null; then echo "$$(go version | awk '{print $$3}')"; \
	  else echo "ABSENT"; fail=1; fi; \
	printf '%-18s' 'git'; \
	  if command -v git >/dev/null; then \
	    v="$$(git --version | awk '{print $$3}')"; \
	    if [ "$$(printf '%s\n%s\n' "$(GIT_MIN_VERSION)" "$$v" | sort -V | head -1)" = "$(GIT_MIN_VERSION)" ]; \
	      then echo "$$v"; else echo "$$v (< $(GIT_MIN_VERSION) requis)"; fail=1; fi; \
	  else echo "ABSENT"; fail=1; fi; \
	printf '%-18s' 'docker'; \
	  if docker info >/dev/null 2>&1; then echo "joignable (testcontainers)"; \
	  else echo "INJOIGNABLE — les tests d'étage 2 échoueront"; fail=1; fi; \
	printf '%-18s' 'kubectl'; \
	  if kubectl version --request-timeout=5s >/dev/null 2>&1; then \
	    echo "cluster: $$(kubectl config current-context)"; \
	  else echo "pas de cluster joignable (étage 3 indisponible)"; fi; \
	printf '%-18s' 'envtest'; \
	  if command -v setup-envtest >/dev/null; then echo "installé"; \
	  else echo "ABSENT — go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest"; fail=1; fi; \
	exit $$fail

.PHONY: lint
lint: ## Lance golangci-lint (arêtes entre rôles serveur, ADR-0006)
	golangci-lint run ./...

.PHONY: help
help: ## Liste les cibles disponibles
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
	  | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

.DEFAULT_GOAL := help

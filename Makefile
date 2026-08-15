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
	printf '%-18s' 'golangci-lint'; \
	  if command -v golangci-lint >/dev/null; then echo "$$(golangci-lint version 2>/dev/null | head -1)"; \
	  else echo "hors du PATH — installé dans $$(go env GOPATH)/bin ? l'ajouter au PATH"; fail=1; fi; \
	printf '%-18s' 'compilateur C'; \
	  if command -v gcc >/dev/null || command -v cc >/dev/null; then echo "présent (détecteur de courses)"; \
	  else echo "ABSENT — \`make test\` échouera : -race exige cgo"; fail=1; fi; \
	printf '%-18s' 'make'; \
	  echo "$$(make --version 2>/dev/null | head -1)"; \
	printf '%-18s' 'envtest'; \
	  if command -v setup-envtest >/dev/null; then echo "installé"; \
	  else echo "ABSENT — go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest"; fail=1; fi; \
	exit $$fail

.PHONY: lint
lint: ## Lance golangci-lint (arêtes entre rôles serveur, ADR-0006)
	golangci-lint run ./...

# Les trois étages de l'ADR-0010. `test` ne réclame ni Docker ni cluster :
# c'est la propriété qui le garde rapide, et un test qui se met à en avoir
# besoin a glissé d'étage.
.PHONY: test
test: ## Étage 1 — tests unitaires, sans Docker ni cluster
	go test -race ./...

.PHONY: test-integration
test-integration: ## Étage 2 — intégration, PostgreSQL via testcontainers
	go test -race -tags=integration ./...

# Étage 3 — un VRAI cluster, par le kubeconfig courant.
#
# Séparé des deux autres et jamais dans `test` : il crée des objets sur le
# cluster que kubectl vise en ce moment. Il se limite à un namespace jetable
# qu'il supprime, mais « jetable » est une propriété du test, pas du cluster —
# personne ne doit le lancer sans savoir où il pointe.
#
# Ce que cet étage établit et que les autres ne peuvent pas : le clientset
# factice accepte n'importe quelle podspec. Il ignore l'admission, le kubelet et
# le montage d'un volume.
.PHONY: test-e2e
test-e2e: ## Étage 3 — e2e sur le cluster du kubeconfig courant (crée des namespaces jetables)
	@echo "Cluster visé : $$(kubectl config current-context)"
	go test -tags=e2e -count=1 ./internal/server/internal/outbound/orchestrator/ ./e2e/

# ---------------------------------------------------------------------------
# Images.
#
# DEUX images et non une, et pas pour une question de taille : l'ADR-0011 pose
# que le binaire du runner tourne dans un domaine hostile et qu'il est lisible
# par un adversaire. Son image ne doit contenir ni le code du control plane, ni
# ses dépendances — une image commune annulerait la séparation que le découpage
# des binaires établit.
# ---------------------------------------------------------------------------

# Le chemin d'un paquet Gitea est `<hôte>/<propriétaire>/<image>`, et le
# propriétaire doit EXISTER — un utilisateur ou une organisation. Un propriétaire
# inventé ne produit pas une erreur d'autorisation mais un 404 à l'envoi du
# premier blob, ce qui se lit comme une panne de registre.
REGISTRY ?= gitea.ruokki.ovh
REGISTRY_OWNER ?= ruokki
IMAGE_TAG ?= dev

IMAGE_BASE := $(REGISTRY)/$(REGISTRY_OWNER)

.PHONY: images
images: ## Construit les deux images en local, sans rien pousser
	docker build -f build/wardend.Dockerfile -t $(IMAGE_BASE)/wardend:$(IMAGE_TAG) .
	docker build -f build/runner.Dockerfile  -t $(IMAGE_BASE)/warden-runner:$(IMAGE_TAG) .

.PHONY: images-push
images-push: images ## Pousse les images (exige un `docker login $(REGISTRY)` préalable)
	docker push $(IMAGE_BASE)/wardend:$(IMAGE_TAG)
	docker push $(IMAGE_BASE)/warden-runner:$(IMAGE_TAG)

# ---------------------------------------------------------------------------
# Base de développement.
#
# Séparée des tests : ceux-ci démarrent leur propre PostgreSQL jetable par
# testcontainers, pour que le même chemin de code serve en local et en CI
# (ADR-0010). Cette base-ci sert à regarder le schéma et à essayer des
# requêtes, pas à faire tourner la CI.
# ---------------------------------------------------------------------------
PG_CONTAINER := warden-dev-pg
PG_PORT      := 5433
PG_IMAGE     := postgres:17-alpine
export WARDEN_DATABASE_URL ?= postgres://postgres:postgres@127.0.0.1:$(PG_PORT)/warden?sslmode=disable

.PHONY: pg-up
pg-up: ## Démarre le PostgreSQL de développement
	@docker start $(PG_CONTAINER) 2>/dev/null || \
	  docker run -d --name $(PG_CONTAINER) \
	    -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB=warden \
	    -p 127.0.0.1:$(PG_PORT):5432 $(PG_IMAGE) >/dev/null
	@printf 'attente de PostgreSQL'; \
	for i in $$(seq 1 30); do \
	  docker exec $(PG_CONTAINER) pg_isready -U postgres -q && break; \
	  printf '.'; sleep 1; \
	done; echo " prêt sur 127.0.0.1:$(PG_PORT)"

.PHONY: pg-down
pg-down: ## Arrête et supprime le PostgreSQL de développement
	@docker rm -f $(PG_CONTAINER) >/dev/null 2>&1 || true

.PHONY: pg-psql
pg-psql: ## Ouvre un psql sur la base de développement
	@docker exec -it $(PG_CONTAINER) psql -U postgres -d warden

.PHONY: migrate
migrate: ## Applique les migrations sur $$WARDEN_DATABASE_URL
	go run ./cmd/wardend migrate

.PHONY: help
help: ## Liste les cibles disponibles
	@grep -hE '^[a-zA-Z0-9_-]+:.*?## ' $(MAKEFILE_LIST) \
	  | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

.DEFAULT_GOAL := help

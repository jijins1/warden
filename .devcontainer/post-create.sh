#!/usr/bin/env bash
# Initialisation du devcontainer warden.
# Idempotent : relancé à chaque reconstruction du conteneur.
set -euo pipefail

echo "==> outils Go"
# Version figée, alignée sur le job `lint` de .github/workflows/ci.yml. Avec
# `@latest`, la CI et le poste de dev ne diraient pas la même chose.
GOLANGCI_VERSION=v2.12.2
go install "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@${GOLANGCI_VERSION}"
# envtest fournit un kube-apiserver et un etcd locaux pour les tests du rôle
# controller (étage 2). Aucun conteneur, aucun cluster.
go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest

echo "==> binaires envtest"
setup-envtest use --bin-dir "$HOME/.local/share/kubebuilder-envtest" -p env > /dev/null

echo "==> kubeconfig"
# Le montage de l'hôte est en lecture seule ; on en fait une copie inscriptible
# pour que `kubectl config use-context` fonctionne sans toucher au fichier de
# l'hôte.
mkdir -p "$HOME/.kube"
if [ -f "$HOME/.kube-host/config" ] && [ ! -f "$HOME/.kube/config" ]; then
  cp "$HOME/.kube-host/config" "$HOME/.kube/config"
  chmod 600 "$HOME/.kube/config"
fi

echo "==> modules Go"
[ -f go.mod ] && go mod download || echo "(pas encore de go.mod)"

echo
echo "Prêt. Vérification de l'environnement : make doctor"

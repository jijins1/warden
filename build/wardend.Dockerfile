# Image du control plane : les trois rôles serveur dans un binaire (ADR-0006).
#
# Elle existe d'abord pour une raison de DÉPLOIEMENT, et accessoirement pour une
# raison de correction : le rôle `git` exige git >= 2.45 (ADR-0005), et jusqu'ici
# rien ne garantissait nulle part qu'il tournerait avec une version qui le
# satisfait. L'exigence était déclarative ; l'image la rend vérifiable.

# `--platform=$BUILDPLATFORM` : cette étape ne fait que compiler du Go, jamais
# rien qui dépende de l'architecture cible. La construire native (jamais sous
# QEMU) et croiser via GOARCH est bien plus rapide qu'émuler tout le compilateur
# — ce qui compte pour un build multi-plateforme, où cette étape tourne deux
# fois (ADR multi-arch).
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build

WORKDIR /src

# Renseignés par buildx d'après `--platform` : absents d'un `docker build`
# classique, où ils retombent sur la plateforme de l'hôte — donc sans effet ici.
ARG TARGETOS
ARG TARGETARCH

# Les dépendances d'abord : elles changent bien moins souvent que le code, et
# cette couche se réutilise entre deux builds.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO désactivé : un binaire statique n'a besoin de rien de l'image de base, ce
# qui laisse le choix de celle-ci à des considérations de sécurité plutôt que de
# compatibilité de bibliothèques.
#
# -trimpath retire les chemins de compilation du binaire. Ils décriraient
# l'arborescence de la machine de build, dans un binaire qui tourne face à du
# code arbitraire.
#
# VERSION est celle que `GET /version` annonce, et que la CLI compare à la
# sienne (ADR-0049). La CI la passe ; un build local reste en `dev`, qui ne
# déclenche aucune mise à jour.
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
      -ldflags="-s -w -X github.com/jijins1/warden/internal/version.Version=${VERSION}" \
      -o /out/wardend ./cmd/wardend


FROM alpine:3.22

# git est le MOTEUR du rôle `git` (ADR-0005) : jamais une réimplémentation Go du
# protocole. ca-certificates sert au fetch vers la forge, qui sort en HTTPS.
#
# git-daemon n'est pas optionnel malgré son nom : sur Alpine, c'est LUI qui
# fournit `git http-backend`, donc tout le smart HTTP — la lecture servie aux
# runners comme l'écriture servie à la CLI. Sans lui, `git` est bien présent, sa
# version satisfait le plancher, et chaque push échoue sur un « HTTP 500 » dont
# la cause réelle est « 'http-backend' is not a git command ».
RUN apk add --no-cache git git-daemon ca-certificates

# Le contrôle de version est fait ICI, à la construction, et pas seulement au
# démarrage du processus. Une image qui ne satisfait pas le plancher de
# l'ADR-0005 ne doit pas exister : la découvrir au déploiement coûte un
# redémarrage en boucle et un incident, la découvrir ici coûte un build rouge.
RUN set -eu; \
    v="$(git --version | sed 's/^git version //')"; \
    maj="${v%%.*}"; reste="${v#*.}"; min="${reste%%.*}"; \
    if [ "$maj" -lt 2 ] || { [ "$maj" -eq 2 ] && [ "$min" -lt 45 ]; }; then \
        echo "git $v < 2.45 requis par l'ADR-0005" >&2; exit 1; \
    fi; \
    echo "git $v"

# La VERSION ne suffit pas : c'est la CAPACITÉ qui compte. Le contrôle précédent
# aurait laissé passer une image où git est conforme mais où le smart HTTP
# n'existe pas — ce qui a d'ailleurs été le premier état de cette image, et ne
# s'est vu qu'au premier push d'un test de bout en bout.
RUN set -eu; \
    git http-backend --help >/dev/null 2>&1 || \
      test -x "$(git --exec-path)/git-http-backend" || { \
        echo "git http-backend absent : le smart HTTP ne fonctionnerait pas" >&2; exit 1; }; \
    echo "git http-backend: présent"

# Utilisateur non privilégié. Le rôle `git` écrit sur son volume : le PVC doit
# lui appartenir, ce qui est une contrainte de déploiement et non un détail —
# un volume monté root rendrait ce conteneur inutilisable en lecture seule.
RUN adduser -D -u 65532 -h /var/lib/warden warden
USER 65532:65532

COPY --from=build /out/wardend /usr/local/bin/wardend

# Pas de CMD : le rôle est un argument obligatoire, et un défaut ferait démarrer
# silencieusement le mauvais composant.
ENTRYPOINT ["/usr/local/bin/wardend"]

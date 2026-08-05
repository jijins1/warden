# Image de développement warden.
#
# Contrainte structurante (ADR-0005) : le binaire `git` est un composant du
# produit, pas un outil de développement. Son comportement varie selon les
# versions (GIT_NAMESPACE, `git maintenance`, commit-graph, protocole v2).
# Cette image et l'image du rôle `git` doivent donc dériver de la **même base
# Debian**, pour que la version de git en dev soit celle qui tourne en
# production. `make doctor` vérifie la version minimale attendue.
FROM mcr.microsoft.com/devcontainers/go:1-trixie

# Version minimale de git exigée par le produit. Doit rester alignée avec
# GIT_MIN_VERSION du Makefile et avec l'image du rôle `git`.
ARG GIT_MIN_VERSION=2.45.0

USER root

RUN apt-get update && apt-get install -y --no-install-recommends \
      git \
      git-lfs \
      ca-certificates \
      postgresql-client \
      jq \
    && rm -rf /var/lib/apt/lists/*

# Échoue à la construction si la base fournit un git trop ancien, plutôt que de
# laisser découvrir la divergence au premier test qui échoue.
RUN installed="$(git --version | awk '{print $3}')" \
    && [ "$(printf '%s\n%s\n' "$GIT_MIN_VERSION" "$installed" | sort -V | head -1)" = "$GIT_MIN_VERSION" ] \
    || { echo "git $installed < $GIT_MIN_VERSION requis"; exit 1; }

USER vscode

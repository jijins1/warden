# Image du runner : l'agent et ce qu'il exécute.
#
# C'est l'image de RÉFÉRENCE. L'ADR-0014 fait déclarer l'image par le projet
# audité, qui épingle du même coup ansible-core, l'interpréteur Python et le
# système — la rejouabilité d'un run dépend donc de la précision de cette
# référence, et le choix appartient au projet. Celle-ci sert de défaut et de base
# de départ.
#
# Elle est SÉPARÉE de celle du control plane, et pas pour une question de taille.
# L'ADR-0011 pose que ce binaire tourne dans un domaine hostile et qu'il est
# lisible par un adversaire : son image ne doit contenir ni le code du control
# plane, ni ses dépendances. Une image commune annulerait la séparation que le
# découpage des binaires établit.

FROM golang:1.26-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/warden-runner ./cmd/warden-runner


FROM alpine:3.22

# ansible-core et non ansible : les collections viennent de l'image déclarée par
# le projet (ADR-0014), pas d'un méta-paquet qui en embarque un jeu arbitraire.
#
# git sert au `fetch` de l'ADR-0007 — le seul accès du pod au code.
# openssh-client sert aux connexions vers les hôtes gérés.
RUN apk add --no-cache ansible-core git openssh-client ca-certificates

# Utilisateur non privilégié, aligné sur le securityContext que l'orchestrateur
# pose : `runAsUser: 65534`. Un écart entre les deux se manifesterait par un pod
# qui refuse de démarrer sur `runAsNonRoot`, message qui ne nomme pas la cause.
USER 65534:65534

# Le workspace est le point de montage attendu par le runner. Il DOIT être un
# volume inscriptible : le pod tourne avec `readOnlyRootFilesystem`, et Ansible
# comme git ont besoin d'écrire. Sans ce volume, l'agent échoue à matérialiser
# son arbre.
# Disposition du volume inscriptible, et elle est réfléchie :
#
#   /workspace       le volume lui-même, qui sert de HOME
#   /workspace/src   l'arbre matérialisé, créé par l'agent
#
# HOME doit être un répertoire QUI EXISTE : le volume est monté vide, et Ansible
# ne crée pas la chaîne de parents. Le faire pointer sur le volume lui-même règle
# cela sans demander à personne de préparer l'arborescence.
#
# L'arbre va dans un sous-répertoire pour qu'il ne se mélange pas aux fichiers de
# travail d'Ansible — un `.ansible/` au milieu d'un arbre audité serait au mieux
# déroutant.
#
# Sans ces variables, Ansible échoue sur « Unable to create local
# directories(/.ansible/tmp) » : un message qui parle de permissions Python et ne
# nomme jamais la cause, l'utilisateur 65534 n'ayant pas de répertoire personnel
# et la racine étant en lecture seule (§7).
ENV HOME=/workspace
ENV WARDEN_WORKSPACE=/workspace/src
ENV ANSIBLE_LOCAL_TEMP=/workspace/.ansible/tmp

COPY --from=build /out/warden-runner /usr/local/bin/warden-runner

# Aucun argument n'est accepté par l'agent : tout ce qu'il doit savoir vient du
# canal de dispatch, dont l'authenticité est établie par son jeton. Un CMD
# inviterait à en passer.
ENTRYPOINT ["/usr/local/bin/warden-runner"]

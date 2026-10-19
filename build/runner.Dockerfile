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

# `--platform=$BUILDPLATFORM` : cette étape ne fait que compiler du Go, jamais
# rien qui dépende de l'architecture cible. La construire native (jamais sous
# QEMU) et croiser via GOARCH est bien plus rapide qu'émuler tout le compilateur
# — ce qui compte pour un build multi-plateforme, où cette étape tourne deux
# fois (ADR multi-arch).
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build

WORKDIR /src

# Renseignés par buildx d'après `--platform` : absents d'un `docker build`
# classique, où ils retombent sur la plateforme de l'hôte — donc sans effet ici.
ARG TARGETOS
ARG TARGETARCH

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/warden-runner ./cmd/warden-runner


FROM alpine:3.22

# ansible-core et non ansible : les collections viennent de l'image déclarée par
# le projet (ADR-0014), pas d'un méta-paquet qui en embarque un jeu arbitraire.
#
# git sert au `fetch` de l'ADR-0007 — le seul accès du pod au code.
# openssh-client sert aux connexions vers les hôtes gérés, et sshpass à celles
# qui s'authentifient par mot de passe (ADR-0048) : Ansible le pilote dès que
# `ansible_password` est posé.
RUN apk add --no-cache ansible-core git openssh-client sshpass ca-certificates py3-pip

# ansible-runner n'est PAS dans les dépôts Alpine : `apk add ansible-core` donne
# le moteur, pas le flux d'events que le §11 demande de réutiliser tel quel. Son
# absence était la dette nommée par l'ADR-0014 — « rien n'impose que l'image
# contienne effectivement ansible-runner » — et elle se manifestait tard, au
# démarrage du run.
#
# --break-system-packages : Alpine 3.22 marque son Python « externally managed »
# (PEP 668). L'avertissement vise les environnements où pip écraserait des
# paquets du système ; ici l'image ne sert qu'à ça et ansible-runner n'est fourni
# par aucun paquet apk, donc il n'y a rien à écraser.
#
# Version épinglée : la rejouabilité d'un run dépend de la précision de cette
# référence (ADR-0014), et le traducteur d'events du runner est écrit contre le
# flux MESURÉ de cette version-là.
RUN pip install --no-cache-dir --break-system-packages ansible-runner==2.4.3

# Le répertoire personnel de 65534 est déplacé sur le workspace, et c'est une
# correction, pas du confort. MESURÉ : Ansible expanse le `~` de son `remote_tmp`
# depuis /etc/passwd, PAS depuis $HOME — le compte `nobody` d'Alpine ayant `/`
# pour home, une tâche en `connection: local` tente de créer `/.ansible/tmp` sur
# une racine en lecture seule (§7) et rend `unreachable`, avec un message qui
# parle de permissions sur la cible.
#
# La corriger ICI plutôt qu'avec ANSIBLE_REMOTE_TEMP est délibéré : cette
# variable s'appliquerait AUSSI aux hôtes gérés, où `~` s'expanse chez la cible
# et où pointer sur /workspace n'aurait aucun sens. L'entrée passwd ne concerne
# que ce conteneur.
#
# Utilisateur non privilégié, aligné sur le securityContext que l'orchestrateur
# pose : `runAsUser: 65534`. Un écart entre les deux se manifesterait par un pod
# qui refuse de démarrer sur `runAsNonRoot`, message qui ne nomme pas la cause.
RUN sed -i 's#^nobody:x:65534:65534:.*#nobody:x:65534:65534:nobody:/workspace:/sbin/nologin#' /etc/passwd
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

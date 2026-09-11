# L'interface web (ADR-0035).
#
# Servie par nginx sur une origine DISTINCTE de l'API, ce qui est la décision
# d'exploitation de l'ADR-0035 : elle impose le CORS, l'anti-CSRF et le
# durcissement de `CheckOrigin` côté serveur, et elle exige que les deux hôtes
# partagent un domaine enregistrable — sans quoi le cookie `SameSite=Strict` ne
# partirait pas.

# --- construction ---------------------------------------------------------
# La version vient de `.nvmrc`, comme la CI. Une seule source de vérité.
#
# `--platform=$BUILDPLATFORM` : `ng build` produit du JS statique, indépendant
# de l'architecture cible. La construire native (jamais sous QEMU) évite
# d'émuler tout un build Angular — ce qui compte pour un build multi-plateforme,
# où cette étape tournerait sinon deux fois.
FROM --platform=$BUILDPLATFORM node:24.20.0-alpine AS build

WORKDIR /src

# Les manifestes d'abord : la couche d'installation ne se réinvalide alors que
# lorsque les dépendances changent, pas à chaque édition de source.
COPY web/ui/package.json web/ui/package-lock.json ./
# `ci` et non `install` : il installe EXACTEMENT le lockfile et échoue s'il a
# dérivé, là où `install` le réécrirait en silence.
RUN npm ci --no-audit --no-fund

COPY web/ui/ ./
RUN npx ng build

# --- exécution ------------------------------------------------------------
# L'image non privilégiée : elle écoute sur 8080 et tourne en uid 101, sans
# qu'il faille défaire quoi que ce soit. Un serveur de fichiers statiques n'a
# aucune raison de démarrer en root, même pour redescendre ensuite.
FROM nginxinc/nginx-unprivileged:1.29-alpine

# La configuration par défaut de l'image sert `/usr/share/nginx/html` en écoutant
# 8080. On la remplace : il faut le repli SPA et les en-têtes de sécurité.
COPY build/ui-nginx.conf /etc/nginx/conf.d/default.conf
COPY build/ui-security-headers.conf /etc/nginx/warden-security-headers.conf

# `connect-src` par défaut : l'application ne peut joindre personne. C'est voulu
# — un défaut permissif marcherait par accident et masquerait une configuration
# absente. L'image nginx substitue les variables des `*.template`, mais ce
# fichier-ci est statique : la valeur est donc posée à la construction, et le
# chart remplace le fragment entier.
RUN sed -i 's/\${WARDEN_CONNECT_SRC}/'"'"'self'"'"'/' /etc/nginx/warden-security-headers.conf

COPY --from=build /src/dist/ui/browser /usr/share/nginx/html

EXPOSE 8080

# Aucune HEALTHCHECK ici : c'est la sonde Kubernetes qui la porte, et en
# dupliquer une dans l'image donnerait deux verdicts pour un seul fait.

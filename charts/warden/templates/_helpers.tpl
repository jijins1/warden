{{/*
Nom de base des ressources.

Avec une release nommée `warden`, tout retombe sur les noms qu'avaient les
manifestes de `deploy/` — `warden-api`, `warden-git`, `warden-db`. C'est
volontaire : une conversion qui renomme les ressources n'est plus une conversion,
c'est une migration, et le PVC des dépôts ne se déplace pas tout seul.
*/}}
{{- define "warden.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
LES ÉTIQUETTES DE SÉLECTION, ET POURQUOI ELLES NE PORTENT QUE DEUX CLÉS.

La convention Helm veut qu'on ajoute `app.kubernetes.io/instance` et
`managed-by` partout. Ici ce serait un défaut, pour deux raisons dont la seconde
ne se voit qu'en production :

 1. Le `selector` d'un Deployment est IMMUABLE. Y ajouter une clé fait échouer
    toute mise à jour d'un déploiement existant avec une erreur de champ
    immuable, et impose de détruire l'objet — donc, pour le rôle `git`, de
    démonter le volume des dépôts.

 2. Les NetworkPolicy sélectionnent sur ces MÊMES étiquettes. Elles matchent par
    sous-ensemble, donc en ajouter n'en casse aucune — mais la politique et le
    pod cessent d'être lisibles côte à côte, et c'est le fichier dont le README
    dit qu'il compte le plus.

Les étiquettes descriptives vont donc sur `metadata.labels` des objets de haut
niveau, JAMAIS dans un `selector` ni dans un `template.metadata.labels`.
*/}}
{{- define "warden.selectorLabels" -}}
app.kubernetes.io/name: warden
app.kubernetes.io/component: {{ .component }}
{{- end -}}

{{/*
Étiquettes descriptives, pour `metadata.labels` uniquement.
*/}}
{{- define "warden.labels" -}}
app.kubernetes.io/name: warden
app.kubernetes.io/component: {{ .component }}
app.kubernetes.io/instance: {{ .root.Release.Name }}
app.kubernetes.io/version: {{ .root.Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .root.Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .root.Chart.Name .root.Chart.Version | replace "+" "_" }}
{{- end -}}

{{/*
Images. Le tag retombe sur `appVersion` pour que la chart et le produit ne se
tiennent pas à la main.
*/}}
{{- define "warden.image" -}}
{{ .Values.image.registry }}/{{ .Values.image.wardend }}:{{ .Values.image.tag | default .Chart.AppVersion }}
{{- end -}}

{{- define "warden.runnerImage" -}}
{{ .Values.image.registry }}/{{ .Values.image.runner }}:{{ .Values.image.tag | default .Chart.AppVersion }}
{{- end -}}

{{/*
Les Secrets de tirage d'images.

Un package GHCR est PRIVÉ par défaut, et un cluster qui n'a pas de quoi
s'authentifier ne le dit pas clairement : le pod reste en `ImagePullBackOff`,
dont le message parle d'une image « not found » — indistinguable d'un tag qui
n'existe pas.

Le Secret n'est PAS créé par la chart : c'est un identifiant de registre, et un
identifiant dans un dépôt git est un identifiant publié. Même raison que
`internalSecret`.
*/}}
{{- define "warden.imagePullSecrets" -}}
{{- with .Values.image.pullSecrets }}
imagePullSecrets:
  {{- range . }}
  - name: {{ . }}
  {{- end }}
{{- end }}
{{- end -}}

{{/*
Le Secret de connexion à la base.

CNPG publie `<cluster>-app`, dont la clé `uri` est directement la chaîne attendue
par le produit. La recomposer à partir de l'hôte, du port et du mot de passe
ajouterait un endroit où elle peut être fausse.
*/}}
{{- define "warden.databaseSecret" -}}
{{- if .Values.database.existingSecret -}}
{{ .Values.database.existingSecret }}
{{- else -}}
{{ include "warden.fullname" . }}-db-app
{{- end -}}
{{- end -}}

{{/*
LES PORTS, et ils ne sont pas dans `values.yaml`.

La décomposition du rôle `git` en trois ports est un CONTRÔLE, pas du rangement :
c'est elle qui permet à une NetworkPolicy de nommer « le port de lecture » sans
nommer les deux autres (ADR-0024, ADR-0027). Les rendre réglables permettrait
d'en faire collisionner deux, et le manifeste continuerait de paraître correct.

Une seule source, pour que le conteneur, le Service et la NetworkPolicy ne
puissent pas diverger.
*/}}
{{- define "warden.port.api" -}}8080{{- end -}}
{{- define "warden.port.gitInternal" -}}8081{{- end -}}
{{- define "warden.port.gitPush" -}}8082{{- end -}}
{{- define "warden.port.gitRead" -}}8083{{- end -}}
{{- define "warden.port.dispatch" -}}8084{{- end -}}
{{- define "warden.port.ingest" -}}8085{{- end -}}
{{- define "warden.port.ui" -}}8080{{- end -}}

{{/*
Image de l'interface web. Distincte de `warden.image` : ce n'est pas le binaire
du control plane mais un nginx qui sert des fichiers statiques, et les deux ne
suivent pas le même cycle de publication.
*/}}
{{- define "warden.uiImage" -}}
{{- $tag := .Values.image.tag | default .Chart.AppVersion -}}
{{ .Values.image.registry }}/{{ .Values.image.ui }}:{{ $tag }}
{{- end -}}

{{/*
L'origine navigateur DÉDUITE de l'ingress de l'UI.
Elle sert à la fois de `WARDEN_UI_ORIGINS` côté API et de `connect-src` côté CSP
— une seule dérivation, pour qu'on ne puisse pas durcir l'un et oublier l'autre
(ADR-0035).
*/}}
{{- define "warden.uiOrigin" -}}
{{- if .Values.ingress.ui.host -}}
https://{{ .Values.ingress.ui.host }}
{{- end -}}
{{- end -}}

{{/*
Vrai (chaîne non vide) quand l'API est servie sous un PRÉFIXE, donc quand
l'interface partage son origine.

Une seule dérivation, consommée par l'ingress, la configuration de l'interface et
la variable du serveur : trois endroits qui doivent dire la même chose, et dont
le désaccord ne se verrait qu'à l'usage — une interface qui charge mais dont
aucun appel n'aboutit.
*/}}
{{- define "warden.origineUnique" -}}
{{- $p := .Values.ingress.ui.apiPath | default "" | trimAll "/" -}}
{{- if and $p .Values.ingress.ui.host }}{{ $p }}{{ end -}}
{{- end -}}

{{/*
Le préfixe NORMALISÉ, barre initiale comprise : `/api`. Vide à la racine.
*/}}
{{- define "warden.basePath" -}}
{{- with include "warden.origineUnique" . }}/{{ . }}{{ end -}}
{{- end -}}

{{- define "warden.apiOrigin" -}}
{{- if .Values.ingress.api.host -}}
https://{{ .Values.ingress.api.host }}
{{- end -}}
{{- end -}}

{{/*
Le `securityContext` du pod, et celui du conteneur.

Ils ne viennent pas de `values.yaml` : ce sont les contrôles du §7, et une valeur
de chart sur un contrôle est une façon de le désactiver en silence.
*/}}
{{- define "warden.podSecurityContext" -}}
runAsNonRoot: true
runAsUser: 65532
runAsGroup: 65532
seccompProfile:
  type: RuntimeDefault
{{- end -}}

{{- define "warden.containerSecurityContext" -}}
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
capabilities:
  drop: ["ALL"]
{{- end -}}

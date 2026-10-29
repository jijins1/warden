package gitwire

// BlobPath lit un fichier dans l'arbre d'un commit, sur le listener INTERNE.
//
// Jamais sur le smart HTTP, comme DiffPath et MirrorPath. La raison est plus
// forte ici que pour les autres : cet endpoint rend le CONTENU d'un fichier
// arbitraire d'un commit arbitraire. L'exposer sur une surface joignable depuis
// un pod runner donnerait à du code arbitraire une lecture du dépôt qui
// contourne le `GIT_NAMESPACE` de l'ADR-0007 — c'est-à-dire toute la réduction
// de portée que ce namespace achète.
//
// Il existe pour l'ADR-0009 : la configuration effective d'un run est résolue
// depuis `<sha>:.warden.yml`, et le rôle `api` n'a aucun accès au
// filesystem git (ADR-0006). Sans cet aller-retour, la CLI resterait la seule à
// lire le fichier, et ce qui s'applique cesserait d'être ce qui est audité.
const BlobPath = "/internal/v1/blob"

// BlobRequest est un ordre git PUR, comme DiffRequest : un dépôt, un commit, un
// chemin.
//
// Il ne nomme ni run ni configuration, et le chemin n'est pas implicite. Le rôle
// `git` ignore que `.warden.yml` existe — savoir QUEL fichier porte la
// configuration d'un run est une règle de run, qui vit chez `api`. Câbler le nom
// ici ferait de `gitd` un lecteur de configuration, et la prochaine règle
// suivrait.
type BlobRequest struct {
	Repo string `json:"repo"`

	// SHA est le commit dont l'arbre est interrogé.
	SHA string `json:"sha"`

	// Path est relatif à la racine de l'arbre. Ni pathspec, ni glob : c'est la
	// syntaxe `<sha>:<chemin>` de git, résolue contre l'arbre du commit.
	Path string `json:"path"`
}

const (
	// ContentTypeBlob : le corps de réponse est le contenu BRUT, pas du JSON.
	// Même raisonnement que ContentTypeDiff — l'encapsuler dans une chaîne JSON
	// l'échapperait et le recopierait des deux côtés pour rien.
	ContentTypeBlob = "application/octet-stream"
)

// MaxBlobBytes borne ce que cet endpoint accepte de servir.
//
// Volontairement PETIT, et sans commune mesure avec MaxDiffBytes. Cet endpoint
// existe pour lire des fichiers de métadonnées d'un commit — quelques dizaines
// de lignes de YAML — sur le chemin d'un run. Le service de fichiers que
// l'ADR-0055 assume (parcourir le code d'un run) vit sur une route DISTINCTE,
// FilePath, avec sa propre borne : élargir celle-ci pour lui ferait accepter au
// lecteur de configuration des contenus qu'il n'a aucune raison de lire.
//
// Elle doit rester ≥ au plafond du schéma de configuration, sans quoi un fichier
// que `config.Parse` accepterait serait refusé une couche plus bas, avec un
// message parlant de transport plutôt que de configuration.
const MaxBlobBytes = 64 << 10

// CodeUnknownPath : le dépôt et le commit existent, le chemin non.
//
// Distinct de CodeUnknownObject, et la distinction porte une décision. Un commit
// absent alors qu'on vient de le résoudre est une incohérence, donc une panne.
// Un CHEMIN absent est un état ordinaire — un projet qui n'a pas encore commité
// de fichier de configuration — que l'appelant doit pouvoir tolérer. Les
// confondre obligerait `core` à traiter une panne comme un cas nominal, ou
// l'inverse.
const CodeUnknownPath Code = "unknown_path"

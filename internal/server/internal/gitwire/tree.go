package gitwire

// TreePath liste les fichiers de l'arbre d'un commit, sur le listener INTERNE.
//
// Il existe pour l'ADR-0055 : parcourir le code d'un run depuis l'interface
// d'audit, au-delà des seules lignes modifiées. Jamais sur le smart HTTP, pour
// la raison donnée sur BlobPath : un pod runner n'a pas à lire le dépôt hors de
// son `GIT_NAMESPACE` (ADR-0007).
const TreePath = "/internal/v1/tree"

// TreeRequest est un ordre git PUR : un dépôt et un commit. Il ne nomme aucun
// run — choisir QUEL commit montrer, et à qui, est une règle de run qui vit
// chez `api`.
type TreeRequest struct {
	Repo string `json:"repo"`
	SHA  string `json:"sha"`
}

// TreeFile est un fichier de l'arbre : son chemin complet depuis la racine, et
// sa taille, qui permet à l'appelant de savoir d'avance qu'un fichier dépasse
// MaxFileBytes.
type TreeFile struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// TreeResponse est la réponse de TreePath.
type TreeResponse struct {
	Files []TreeFile `json:"files"`

	// Truncated dit que la liste a été coupée à MaxTreeEntries. Une liste
	// coupée qui se présenterait complète ferait conclure à l'absence des
	// fichiers au-delà de la borne.
	Truncated bool `json:"truncated"`
}

// MaxTreeEntries borne la liste. Un dépôt Ansible en compte quelques
// centaines ; la borne protège le pod `git`, en instance unique (ADR-0006), et
// la page d'audit d'un dépôt qui embarquerait un vendoring entier.
const MaxTreeEntries = 20000

// FilePath rend le contenu d'un fichier de l'arbre d'un commit, pour le
// parcours de l'ADR-0055.
//
// Distinct de BlobPath, et c'est le point : les deux lisent un blob de la même
// manière, mais pas pour le même usage ni sous la même borne. Le lecteur de
// configuration garde sa borne de 64 Ko ; celui-ci accepte un fichier source
// ordinaire.
const FilePath = "/internal/v1/file"

// FileRequest a la forme de BlobRequest : un dépôt, un commit, un chemin relatif
// à la racine de l'arbre, sans pathspec ni glob.
type FileRequest struct {
	Repo string `json:"repo"`
	SHA  string `json:"sha"`
	Path string `json:"path"`
}

// MaxFileBytes borne un fichier servi par FilePath. Au-delà, un fichier se
// lit mal dans une page d'audit et coûte au pod `git` sans servir personne.
const MaxFileBytes = 1 << 20

// CodeTooLarge : le fichier existe mais dépasse la borne de la route.
//
// Distinct de CodeBadRequest : ce n'est pas un défaut de l'appelant, c'est un
// état du dépôt, que l'interface dit tel quel plutôt que comme une panne.
const CodeTooLarge Code = "too_large"

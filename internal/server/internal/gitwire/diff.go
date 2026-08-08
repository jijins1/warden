package gitwire

// DiffPath est servi sur le listener INTERNE du rôle `git`, distinct de celui du
// smart HTTP joignable par les runners (ADR-0006, ADR-0007).
const DiffPath = "/internal/v1/diff"

// DiffRequest est un ordre git PUR : deux objets et le dépôt où les trouver.
//
// Il ne nomme aucun run, et c'est ce qui compte. Le rôle `git` ignore le modèle
// d'audit : « quel est le run précédent » se tranche du côté `api`, qui détient
// la politique d'accès. Un `gitd` qui résoudrait lui-même le run précédent
// court-circuiterait cette politique, alors que le run précédent appartient
// souvent à quelqu'un d'autre et que son arbre est son code.
//
// Corollaire utile : la surface exposée par `gitd` reste minimale, ce que son
// doc exige puisqu'il est joignable depuis du code arbitraire (ADR-0007).
type DiffRequest struct {
	Repo string `json:"repo"`
	From string `json:"from"`
	To   string `json:"to"`
}

const (
	// ContentTypeDiff : le corps de réponse est le diff BRUT, pas du JSON.
	// L'encapsuler dans une chaîne JSON l'échapperait et le recopierait des deux
	// côtés, sur plusieurs mégaoctets, pour aucun bénéfice.
	ContentTypeDiff = "text/plain; charset=utf-8"

	// ContentTypeError : seules les erreurs sont en JSON.
	ContentTypeError = "application/json"

	// HeaderTruncated signale un diff coupé à MaxDiffBytes.
	//
	// Tronquer en silence serait un mensonge dans un journal d'audit : un
	// lecteur conclurait que rien d'autre n'a changé.
	HeaderTruncated = "X-Warden-Diff-Truncated"
)

// MaxDiffBytes borne ce qui traverse l'arête.
//
// Un blob binaire de quelques centaines de mégaoctets dans le dépôt audité ne
// doit tuer ni le pod `git`, qui est en instance unique (ADR-0006), ni un
// réplica `api`. La borne est appliquée au plus tôt, à la lecture de la sortie
// de git, et non au moment d'écrire la réponse.
const MaxDiffBytes = 8 << 20

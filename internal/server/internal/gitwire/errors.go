package gitwire

// Code classe les échecs de l'API interne.
//
// Le client classe sur le CODE et non sur le statut HTTP : deux causes bien
// distinctes pour l'audit — dépôt inconnu et objet absent — partagent le 404, et
// la distinction se perdrait à la traduction.
type Code string

const (
	// CodeBadRequest : dépôt ou SHA malformé. C'est un défaut de l'appelant,
	// donc de nous : les deux extrémités sont du même produit.
	CodeBadRequest Code = "bad_request"

	// CodeUnknownRepo : aucun dépôt de ce nom sous la racine.
	CodeUnknownRepo Code = "unknown_repo"

	// CodeUnknownObject : le dépôt existe, l'objet non. C'est le cas « code
	// purgé » de l'ADR-0013, que l'interface d'audit doit traiter en première
	// classe plutôt que de renvoyer un 404 qui se lirait « run inconnu ».
	CodeUnknownObject Code = "unknown_object"

	// CodeInternal : échec du moteur git ou du service.
	CodeInternal Code = "internal"
)

// Error est le SEUL corps d'erreur du contrat.
//
// Le message est rédigé côté serveur et ne contient jamais la sortie brute de
// git : sa stderr cite volontiers des chemins du dépôt audité, qui n'ont rien à
// faire dans la réponse d'un appelant.
type Error struct {
	Code    Code   `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return string(e.Code) + ": " + e.Message }

// HTTPStatus rend le statut associé à un code.
//
// La correspondance vit ici, dans le contrat, pour que les deux extrémités ne
// puissent pas en avoir deux lectures différentes.
func (c Code) HTTPStatus() int {
	switch c {
	case CodeBadRequest:
		return 400
	case CodeUnknownRepo, CodeUnknownObject, CodeUnknownPath:
		return 404
	case CodeMirrorFailed:
		// 502 et non 500 : la panne est CHEZ LA FORGE, pas chez nous. La
		// distinction est ce qui évite qu'un opérateur passe une heure dans les
		// journaux du rôle `git` alors que c'est le dépôt amont qui répond mal.
		return 502
	case CodeRefExists:
		// 409 : ni un défaut de l'appelant ni une panne du moteur — un
		// identifiant de run réutilisé sur un commit différent de celui déjà
		// enregistré. Une collision, pas une erreur.
		return 409
	case CodeTooLarge:
		return 413
	default:
		return 500
	}
}

// CodeFromStatus est le repli du client quand le corps n'est pas décodable.
// Il ne peut pas distinguer les deux 404 : c'est précisément pourquoi le code
// voyage dans le corps.
func CodeFromStatus(status int) Code {
	switch status {
	case 400:
		return CodeBadRequest
	case 404:
		return CodeUnknownObject
	case 502:
		return CodeMirrorFailed
	case 409:
		return CodeRefExists
	case 413:
		return CodeTooLarge
	default:
		return CodeInternal
	}
}

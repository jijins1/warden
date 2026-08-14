package gitwire

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// PushGrant est la portée d'écriture qu'un jeton ouvre, transportée DANS le
// jeton lui-même.
//
// Le rôle `git` ne parle pas à PostgreSQL et c'est délibéré (ADR-0024). La
// portée d'un push doit pourtant venir du rôle `api`, seul détenteur de la
// politique. Deux façons de franchir cet écart : un état répliqué dans `gitd`,
// avec son cycle de vie, sa purge et ses runs en vol perdus au redémarrage — ou
// un jeton qui se décrit lui-même et que sa signature rend infalsifiable.
// L'ADR-0028 retient la seconde : elle ne stocke rien, ne synchronise rien, et
// laisse `gitd` sans état.
//
// Ce que le format ne fait pas, et qui est assumé : il n'y a **aucune
// révocation** avant expiration. Ce qui rend cela tenable n'est pas une intention
// mais deux propriétés déjà imposées ailleurs — la portée est bornée au namespace
// d'audit de son porteur, et une ref d'audit est écrite une fois (ADR-0027). Un
// jeton rejoué ne peut donc ni lire ni écraser le travail d'autrui ; il peut
// seulement créer des refs inertes chez lui.
type PushGrant struct {
	// Repo est l'identifiant de dépôt, tel que le rôle `git` le résout.
	Repo string `json:"r"`

	// RefPrefix borne les refs que ce jeton autorise à créer, typiquement
	// `refs/audit/<utilisateur>/`.
	//
	// Un PRÉFIXE et non une ref exacte, et c'est ce qui permet à la CLI de tirer
	// son propre identifiant de run au lieu d'aller le demander. L'appel de
	// réservation disparaît, et avec lui la création de run en deux temps et le
	// réordonnancement du §5.2.
	//
	// Ce que le relâchement coûte, dit franchement : un jeton rejoué laisse créer
	// des refs arbitraires DANS LE NAMESPACE DE SON PROPRE PORTEUR. Il ne permet
	// toujours ni de lire ni d'écraser celui d'autrui — le contrôle de fraîcheur,
	// l'écriture unique et le masquage des refs tiennent tous trois sans
	// changement (ADR-0027).
	//
	// La principale n'est PAS ici. C'est une propriété du dépôt, pas de l'octroi :
	// elle ne varie jamais d'un push à l'autre, et l'inscrire dans chaque jeton
	// créait une seconde source de vérité pour la même chose. Le rôle `git` la lit
	// dans le dépôt, où le rafraîchissement du miroir l'écrit.
	RefPrefix string `json:"p"`

	// Expires est une date Unix en secondes.
	Expires int64 `json:"x"`
}

// grantVersion préfixe le jeton et entre dans le calcul de la signature.
//
// La séparation de domaine n'est pas cosmétique : le secret qui signe est celui
// qui authentifie déjà l'API interne (ADR-0024). Sans préfixe distinct, une
// chaîne valide pour l'un pourrait un jour être présentée à l'autre. Le préfixe
// rend les deux usages non interchangeables par construction.
const grantVersion = "wpg1"

// ErrGrantInvalid est le SEUL échec rendu par ParsePushGrant.
//
// Ni « signature fausse », ni « expiré », ni « corps illisible ». L'appelant est
// `gitd`, joignable depuis du domaine hostile, et chacune de ces distinctions
// est une information offerte à qui sonde — la même posture que le middleware de
// l'API interne, pour la même raison.
var ErrGrantInvalid = errors.New("jeton de push invalide")

// SignPushGrant frappe un jeton porteur de sa propre portée.
func SignPushGrant(g PushGrant, secret string) (string, error) {
	if secret == "" {
		return "", errors.New("secret de signature absent : le jeton serait forgeable")
	}
	if g.Repo == "" || g.RefPrefix == "" {
		return "", errors.New("portée incomplète : dépôt et préfixe de ref sont obligatoires")
	}
	// Un préfixe qui ne finit pas par `/` autorise plus que ce que son auteur
	// croit : `refs/audit/bob` couvrirait `refs/audit/bobby/...`. Le refus est ici
	// plutôt qu'à la vérification, du côté où la faute se corrige.
	if !strings.HasSuffix(g.RefPrefix, "/") {
		return "", errors.New("préfixe de ref sans barre finale : il déborderait sur les namespaces voisins")
	}
	if g.Expires <= 0 {
		return "", errors.New("portée sans expiration : le jeton serait éternel")
	}

	corps, err := json.Marshal(g)
	if err != nil {
		return "", fmt.Errorf("encodage de la portée : %w", err)
	}
	charge := base64.RawURLEncoding.EncodeToString(corps)

	return grantVersion + "." + charge + "." +
		base64.RawURLEncoding.EncodeToString(signature(charge, secret)), nil
}

// ParsePushGrant vérifie un jeton et rend la portée qu'il déclare.
//
// L'ordre compte : la signature est vérifiée AVANT que le corps soit décodé.
// Décoder d'abord ferait passer du JSON non authentifié par l'analyseur, ce qui
// est une surface offerte gratuitement à un appelant hostile.
func ParsePushGrant(token, secret string) (PushGrant, error) {
	if secret == "" {
		return PushGrant{}, errors.New("secret de vérification absent")
	}

	version, reste, ok := strings.Cut(token, ".")
	if !ok || version != grantVersion {
		return PushGrant{}, ErrGrantInvalid
	}
	charge, sig, ok := strings.Cut(reste, ".")
	if !ok {
		return PushGrant{}, ErrGrantInvalid
	}

	presentee, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return PushGrant{}, ErrGrantInvalid
	}
	// Comparaison en temps constant. Un `==` sur des octets s'arrête au premier
	// écart, et la durée du refus décrit alors combien de préfixe était juste —
	// ce qui rend une signature devinable octet par octet.
	if !hmac.Equal(presentee, signature(charge, secret)) {
		return PushGrant{}, ErrGrantInvalid
	}

	corps, err := base64.RawURLEncoding.DecodeString(charge)
	if err != nil {
		return PushGrant{}, ErrGrantInvalid
	}
	var g PushGrant
	dec := json.NewDecoder(strings.NewReader(string(corps)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&g); err != nil {
		return PushGrant{}, ErrGrantInvalid
	}

	if g.Repo == "" || g.RefPrefix == "" || !strings.HasSuffix(g.RefPrefix, "/") {
		return PushGrant{}, ErrGrantInvalid
	}
	if time.Now().Unix() >= g.Expires {
		return PushGrant{}, ErrGrantInvalid
	}
	return g, nil
}

func signature(charge, secret string) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	// La version entre dans le calcul, pas seulement dans le préfixe : sans
	// cela, un futur format v2 au même corps aurait la même signature, et un
	// jeton pourrait être rétrogradé vers la version la moins stricte.
	mac.Write([]byte(grantVersion))
	mac.Write([]byte("."))
	mac.Write([]byte(charge))
	return mac.Sum(nil)
}

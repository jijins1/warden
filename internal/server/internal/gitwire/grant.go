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

// Genres de jetons. Le genre préfixe le jeton ET entre dans le calcul de la
// signature.
//
// La séparation de domaine n'est pas cosmétique. Un même secret signe désormais
// trois choses : l'authentification de l'API interne (ADR-0024), l'autorisation
// d'écriture, et l'autorisation de lecture. Sans genre distinct, un jeton frappé
// pour l'une pourrait être présenté à l'autre — et le porteur d'un jeton de
// lecture est un pod runner, c'est-à-dire du code arbitraire (§2). Il ne doit pas
// pouvoir se présenter comme un pousseur.
const (
	genrePush  = "wpg1"
	genreFetch = "wfg1"
)

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

	return signer(genrePush, g, secret)
}

// signer frappe un jeton d'un genre donné.
func signer(genre string, portee any, secret string) (string, error) {
	corps, err := json.Marshal(portee)
	if err != nil {
		return "", fmt.Errorf("encodage de la portée : %w", err)
	}
	charge := base64.RawURLEncoding.EncodeToString(corps)
	return genre + "." + charge + "." +
		base64.RawURLEncoding.EncodeToString(signature(genre, charge, secret)), nil
}

// verifier vérifie un jeton et rend sa charge décodée.
//
// L'ordre compte : la signature est vérifiée AVANT que le corps soit décodé.
// Décoder d'abord ferait passer du JSON non authentifié par l'analyseur, ce qui
// est une surface offerte gratuitement à un appelant hostile.
func verifier(genre, token, secret string, sortie any) error {
	if secret == "" {
		return errors.New("secret de vérification absent")
	}

	presente, reste, ok := strings.Cut(token, ".")
	if !ok || presente != genre {
		return ErrGrantInvalid
	}
	charge, sig, ok := strings.Cut(reste, ".")
	if !ok {
		return ErrGrantInvalid
	}

	brute, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return ErrGrantInvalid
	}
	// Comparaison en temps constant. Un `==` sur des octets s'arrête au premier
	// écart, et la durée du refus décrit alors combien de préfixe était juste —
	// ce qui rend une signature devinable octet par octet.
	if !hmac.Equal(brute, signature(genre, charge, secret)) {
		return ErrGrantInvalid
	}

	corps, err := base64.RawURLEncoding.DecodeString(charge)
	if err != nil {
		return ErrGrantInvalid
	}
	dec := json.NewDecoder(strings.NewReader(string(corps)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(sortie); err != nil {
		return ErrGrantInvalid
	}
	return nil
}

// ParsePushGrant vérifie un jeton et rend la portée qu'il déclare.
//
// L'ordre compte : la signature est vérifiée AVANT que le corps soit décodé.
// Décoder d'abord ferait passer du JSON non authentifié par l'analyseur, ce qui
// est une surface offerte gratuitement à un appelant hostile.
func ParsePushGrant(token, secret string) (PushGrant, error) {
	var g PushGrant
	if err := verifier(genrePush, token, secret, &g); err != nil {
		return PushGrant{}, err
	}
	if g.Repo == "" || g.RefPrefix == "" || !strings.HasSuffix(g.RefPrefix, "/") {
		return PushGrant{}, ErrGrantInvalid
	}
	if time.Now().Unix() >= g.Expires {
		return PushGrant{}, ErrGrantInvalid
	}
	return g, nil
}

func signature(genre, charge, secret string) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	// Le genre entre dans le CALCUL, pas seulement dans le préfixe. Sans cela,
	// un jeton de lecture et un jeton d'écriture au même corps auraient la même
	// signature : il suffirait d'en changer le préfixe pour convertir l'un en
	// l'autre. Un futur format v2 serait rétrogradable de la même façon.
	mac.Write([]byte(genre))
	mac.Write([]byte("."))
	mac.Write([]byte(charge))
	return mac.Sum(nil)
}

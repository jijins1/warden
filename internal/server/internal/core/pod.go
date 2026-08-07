package core

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"
)

// podTokenBytes est la taille de l'aléa d'un jeton d'amorçage (ADR-0023).
//
// 256 bits. Il n'y a rien à économiser ici : le jeton est tiré une fois par pod,
// transmis une fois, et sa seule autre existence est une empreinte de 32 octets.
const podTokenBytes = 32

// ErrInvalidPodToken est renvoyée par ParsePodToken sur une valeur malformée.
var ErrInvalidPodToken = errors.New("jeton d'amorçage invalide")

// PodToken est l'identité d'amorçage d'un pod runner (ADR-0023).
//
// Sa capacité unique est « je suis le pod chaud N » : il ne nomme aucun run, ne
// permet aucune lecture, et ne vaut rien tant que le pod n'est pas réclamé. Le
// serveur résout `pod -> run` depuis sa propre base ; un `run_id` reçu dans un
// body de requête n'est jamais la source.
//
// Le type vit dans le cœur et non dans un adaptateur PARCE QUE deux adaptateurs
// en dépendent — `store` en conserve l'empreinte, `dispatch` authentifie avec —
// et que l'ADR-0019 leur interdit de se connaître. C'est le cas d'école du type
// de domaine partagé.
//
// La valeur est enveloppée dans une structure à champ non exporté : elle ne peut
// donc être construite que par ce paquet, et les trois chemins d'impression
// usuels (String, GoString, JSON) la remplacent par un marqueur. Journaliser un
// jeton par accident demande un effort délibéré.
type PodToken struct {
	v string
}

// tokenMask remplace la valeur partout où elle pourrait être imprimée.
//
// Le nom évite délibérément les mots que gosec surveille dans les identifiants
// porteurs de littéraux : la constante ne contient aucun secret, et un
// `//nolint` posé ici s'appliquerait aussi à ce qui viendrait s'ajouter autour.
// nolint:gosec
const tokenMask = "[jeton d'amorçage supprimé]"

// NewPodToken tire un jeton d'amorçage.
func NewPodToken() (PodToken, error) {
	b := make([]byte, podTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return PodToken{}, fmt.Errorf("tirage du jeton d'amorçage: %w", err)
	}
	return PodToken{v: base64.RawURLEncoding.EncodeToString(b)}, nil
}

// ParsePodToken relit un jeton présenté par un pod.
//
// La validation de forme n'est PAS un contrôle de sécurité — celui-ci est la
// recherche par empreinte. Elle évite seulement d'aller interroger la base pour
// une valeur qui ne peut de toute façon correspondre à rien.
func ParsePodToken(s string) (PodToken, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(b) != podTokenBytes {
		return PodToken{}, ErrInvalidPodToken
	}
	return PodToken{v: s}, nil
}

// Reveal rend la valeur en clair.
//
// Un seul appelant légitime : l'écriture du Secret Kubernetes monté dans le pod.
// Le nom est délibérément inconfortable — il doit se voir en revue.
func (t PodToken) Reveal() string { return t.v }

// Hash est l'empreinte SHA-256, seule forme stockée côté serveur.
//
// L'authentification est une recherche par empreinte et non une comparaison de
// jetons : aucun secret n'est comparé octet à octet, et compromettre la base ne
// donne aucune identité de pod.
func (t PodToken) Hash() []byte {
	sum := sha256.Sum256([]byte(t.v))
	return sum[:]
}

// Zero dit si le jeton est le jeton nul.
func (t PodToken) Zero() bool { return t.v == "" }

// String masque la valeur. C'est ce qui protège des `%s` et `%v`.
func (t PodToken) String() string { return tokenMask }

// GoString masque la valeur sous `%#v`, que String ne couvre pas.
func (t PodToken) GoString() string { return tokenMask }

// MarshalJSON masque la valeur.
//
// Rien ne sérialise légitimement un jeton d'amorçage : le pod le lit depuis un
// fichier et le présente en en-tête, il ne transite dans aucun body JSON. Le
// masquage couvre le cas où une structure englobante finirait par en contenir un
// sans que personne l'ait voulu.
func (t PodToken) MarshalJSON() ([]byte, error) {
	return []byte(`"` + tokenMask + `"`), nil
}

// PodIdentity est ce qu'une authentification de pod établit.
//
// `RunID` est nul tant que le pod n'est pas réclamé, et c'est le cas NORMAL : un
// pod chaud passe l'essentiel de son existence authentifié et sans run. Il ne
// doit alors rien pouvoir obtenir — c'est le test négatif que l'ADR-0018 exige
// pour son troisième port.
type PodIdentity struct {
	PodID int64
	// RunID est 0 tant qu'aucun run n'est attribué.
	RunID int64
}

// Claimed dit si un run est attribué à ce pod.
func (p PodIdentity) Claimed() bool { return p.RunID != 0 }

// PodPool est la clé de pré-chauffage du §8.3.
//
// Un pod n'est éligible qu'aux runs du même triplet. C'est ce qui interdit qu'un
// pod chaud d'un utilisateur serve le run d'un autre, et ce qui rend le « cache
// Ansible jamais partagé entre utilisateurs » du §7 vrai par construction plutôt
// que par configuration.
type PodPool struct {
	UserID      int64
	Repo        string
	Environment string
}

// PodState est l'état d'un pod tel que le routage du dispatch le lit.
//
// C'est le résultat du « pointeur » de l'ADR-0018 amendée : la notification dit
// seulement quel pod regarder, cette structure est ce qu'on trouve en
// regardant. Elle ne contient AUCUN credential — ils sont lus au moment de la
// livraison, et ne sont donc jamais persistés.
type PodState struct {
	PodID int64
	// RunID est 0 tant qu'aucun run n'est attribué.
	RunID             int64
	CancelRequestedAt time.Time
	ReleasedAt        time.Time
}

// Claimed dit si un run est attribué à ce pod.
func (p PodState) Claimed() bool { return p.RunID != 0 }

// CancelRequested dit si l'annulation du §5.6 a été demandée.
func (p PodState) CancelRequested() bool { return !p.CancelRequestedAt.IsZero() }

// Released dit si le pod est définitivement hors service (§8.2 : jamais
// réutilisé).
func (p PodState) Released() bool { return !p.ReleasedAt.IsZero() }

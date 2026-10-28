package core

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"
)

// sessionTokenBytes est la taille de l'aléa d'un jeton de session.
//
// 256 bits, comme le jeton d'amorçage. Il n'y a rien à économiser : il est tiré
// une fois par connexion et sa seule autre existence est une empreinte.
const sessionTokenBytes = 32

// ErrJetonInvalide est renvoyée par ParseSessionToken sur une valeur malformée.
var ErrJetonInvalide = errors.New("jeton de session invalide")

// SessionToken est ce que `warden login` rend et que la CLI présente ensuite.
//
// Même patron que PodToken, et pour les mêmes raisons : valeur enveloppée dans
// une structure à champ non exporté — donc inconstructible hors de ce paquet —
// et les trois chemins d'impression usuels remplacés par un marqueur.
// Journaliser un jeton de session par accident demande un effort délibéré.
//
// La différence avec PodToken est le SENS : celui-ci porte l'identité d'un
// humain, alors que le jeton d'amorçage ne dit que « je suis le pod N ». C'est
// pourquoi il expire, se révoque, et n'est jamais écrit ailleurs que dans un
// fichier à 0600 sur le poste de son propriétaire.
type SessionToken struct {
	v string
}

// sessionMask remplace la valeur partout où elle pourrait être imprimée.
// nolint:gosec
const sessionMask = "[jeton de session supprimé]"

// NewSessionToken tire un jeton de session.
func NewSessionToken() (SessionToken, error) {
	b := make([]byte, sessionTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return SessionToken{}, fmt.Errorf("tirage du jeton de session: %w", err)
	}
	return SessionToken{v: base64.RawURLEncoding.EncodeToString(b)}, nil
}

// ParseSessionToken relit un jeton présenté par un client.
//
// La validation de forme n'est PAS le contrôle de sécurité — celui-ci est la
// recherche par empreinte. Elle évite d'aller interroger la base pour une valeur
// qui ne peut de toute façon correspondre à rien, ce qui compte sur un endpoint
// que n'importe qui peut marteler.
func ParseSessionToken(s string) (SessionToken, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(b) != sessionTokenBytes {
		return SessionToken{}, ErrJetonInvalide
	}
	return SessionToken{v: s}, nil
}

// Reveal rend la valeur en clair.
//
// Deux appelants légitimes et pas un de plus : la réponse à `warden login`, et
// l'en-tête `Authorization` que la CLI compose. Le nom est délibérément
// inconfortable — il doit se voir en revue.
func (t SessionToken) Reveal() string { return t.v }

// Hash est l'empreinte SHA-256, seule forme stockée côté serveur.
//
// SHA-256 nu et non argon2, alors que le mot de passe exige argon2 : la
// différence est l'ENTROPIE. Un mot de passe humain a quelques dizaines de bits
// et doit être rendu coûteux à essayer ; ce jeton en a 256, tirés au hasard, et
// aucune quantité de calcul ne le retrouve. Lui appliquer argon2 coûterait
// 19 Mio à chaque requête authentifiée pour ne rien acheter.
func (t SessionToken) Hash() []byte {
	sum := sha256.Sum256([]byte(t.v))
	return sum[:]
}

// Zero dit si le jeton est le jeton nul.
func (t SessionToken) Zero() bool { return t.v == "" }

// String masque la valeur. C'est ce qui protège des `%s` et `%v`.
func (t SessionToken) String() string { return sessionMask }

// GoString masque la valeur sous `%#v`, que String ne couvre pas.
func (t SessionToken) GoString() string { return sessionMask }

// MarshalJSON masque la valeur.
//
// Le jeton DOIT pourtant sortir en JSON une fois — dans la réponse à
// `warden login`. Ce chemin-là appelle `Reveal` explicitement et compose la
// chaîne lui-même ; le masquage couvre tous les autres, c'est-à-dire les
// structures qui finiraient par en contenir un sans que personne l'ait voulu.
func (t SessionToken) MarshalJSON() ([]byte, error) {
	return []byte(`"` + sessionMask + `"`), nil
}

// DureeSession est la validité d'une session, à défaut de configuration.
//
// Trente jours. C'est un arbitrage explicite entre deux torts : une session
// courte fait retaper un mot de passe plusieurs fois par jour, ce qui pousse à
// le choisir mauvais ou à contourner l'outil — or « un point de contrôle qu'on
// contourne ne contrôle rien » est la thèse du produit. Une session longue laisse
// un jeton exploitable traîner sur un poste.
//
// Le curseur est mis du côté de l'usage PARCE QUE la révocation existe :
// `warden logout`, la désactivation d'un compte et le changement de mot de passe
// coupent toutes les sessions immédiatement.
const DureeSession = 30 * 24 * time.Hour

// Session est ce qu'une authentification par jeton établit.
type Session struct {
	ID     int64
	UserID int64

	// Groupes sont ceux de l'annuaire, figés à la connexion (ADR-0053) ; vide
	// pour un compte local.
	Groupes []string

	ExpiresAt  time.Time
	LastSeenAt time.Time
}

// SessionStore range et relit les sessions.
type SessionStore interface {
	// CreateSession enregistre une session et rend son identifiant.
	//
	// `groupes` sont ceux que l'annuaire a rendus à la connexion, normalisés ;
	// nil pour un compte local. Ils sont figés avec la session et ne changent
	// plus ensuite : c'est la durée de la session qui borne la prise d'effet
	// d'un retrait de groupe (ADR-0053, décision 3).
	CreateSession(ctx context.Context, userID int64, hash []byte, expire time.Time, groupes []string) (int64, error)

	// SessionByToken résout un jeton VIVANT en session et utilisateur.
	//
	// La session rendue porte ses groupes. `User.IsAdmin` est le seul drapeau
	// du compte : l'administration par groupe est calculée par le cœur.
	//
	// Une session expirée ou révoquée est ErrNotFound et non une session qu'on
	// rendrait « invalide » : le seul appelant est le middleware, et un état
	// intermédiaire l'inviterait à décider lui-même de ce qui est encore valable.
	SessionByToken(ctx context.Context, hash []byte) (Session, User, error)

	// RevokeSession coupe une session. Idempotent.
	RevokeSession(ctx context.Context, id int64) error

	// TouchSession rafraîchit `last_seen_at`, PARESSEUSEMENT : elle n'écrit que
	// si la valeur enregistrée est plus vieille que `age`. C'est ce qui évite une
	// écriture sur chaque requête authentifiée.
	TouchSession(ctx context.Context, id int64, age time.Duration) error
}

// CredentialStore range et relit les mots de passe des comptes locaux.
type CredentialStore interface {
	// EmpreinteDuCompte rend l'empreinte du compte local, ou ErrNotFound.
	EmpreinteDuCompte(ctx context.Context, subject string) (User, string, error)

	// PoserMotDePasse écrit l'empreinte et RÉVOQUE toutes les sessions du
	// compte, dans la même transaction. Les deux vont ensemble : changer un mot
	// de passe parce qu'on le croit compromis sans couper les sessions ouvertes
	// ne protège de rien.
	PoserMotDePasse(ctx context.Context, userID int64, empreinte string, parQui int64) error
}

// LocalAccount est un compte local tel que l'administration le liste.
//
// `HasPassword` est la colonne qui compte à l'exploitation : un compte créé dont
// personne n'a jamais posé le mot de passe existe, apparaît, et ne peut pas se
// connecter. Sans elle, il se lit comme un compte cassé.
type LocalAccount struct {
	User
	HasPassword bool
	LastSeenAt  time.Time
}

// LocalAccounts administre les comptes locaux.
//
// Séparé de `UserDirectory`, qui ne sait que RÉSOUDRE une identité déjà
// provisionnée : ce port-ci crée et désactive, et il ne doit être branché que
// sur les chemins qui ont établi l'autorisation.
type LocalAccounts interface {
	CreateLocalUser(ctx context.Context, subject, nom string, admin bool) (User, error)
	SetUserDisabled(ctx context.Context, subject string, desactive bool) (User, error)
	ListLocalUsers(ctx context.Context) ([]LocalAccount, error)
}

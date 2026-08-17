package core

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// AccessKey est le credential d'accès remis à un runner (ADR-0020).
//
// La valeur est enveloppée dans une structure à champ non exporté, comme
// `PodToken` : elle ne peut donc être construite que par ce paquet, et les trois
// chemins d'impression usuels la remplacent par un marqueur. Journaliser une clé
// privée par accident demande un effort délibéré.
//
// Elle ne porte AUCUN nom d'environnement ni d'hôte : ce qui identifie une clé
// est la question qu'on a posée pour l'obtenir, pas la valeur qu'on a reçue.
type AccessKey struct {
	v []byte

	// Expiry est la fin de validité, NULLE en v1.
	//
	// Le champ est là parce que l'ADR-0020 l'a mis dans la signature : il ne
	// sert à rien à l'adaptateur Kubernetes, et c'est exactement ce qui rendra
	// l'adaptateur Vault indolore, la validité d'un certificat étant bornée.
	// Le paramètre inutile aujourd'hui est gratuit ; l'ajouter plus tard ne
	// l'est pas.
	Expiry time.Time
}

// nolint:gosec
const keyMask = "[clé d'accès supprimée]"

// NewAccessKey enveloppe une partie privée.
//
// Exportée parce que l'adaptateur qui lit le Secret vit dans `outbound`, et que
// l'ADR-0019 lui interdit d'être dans le cœur. C'est le prix du champ non
// exporté, et il vaut le coup : sans lui, n'importe quel `%v` sur une structure
// englobante imprimerait la clé.
func NewAccessKey(private []byte, expiry time.Time) AccessKey {
	return AccessKey{v: private, Expiry: expiry}
}

// Reveal rend la valeur en clair. Nom délibérément inconfortable : il doit se
// voir en revue.
func (k AccessKey) Reveal() []byte { return k.v }

// Zero dit si la clé est la clé nulle.
func (k AccessKey) Zero() bool { return len(k.v) == 0 }

// String masque la valeur. C'est ce qui protège des `%s` et `%v`.
func (k AccessKey) String() string { return keyMask }

// GoString masque la valeur sous `%#v`, que String ne couvre pas.
func (k AccessKey) GoString() string { return keyMask }

// MarshalJSON masque la valeur.
//
// Contrairement au `Secret` du contrat de fil, rien ne sérialise légitimement ce
// type : la clé descend vers le pod par `wire/dispatch.Secret`, qui est un autre
// type, dans un autre domaine. Le masquage couvre donc le cas où une structure
// englobante finirait par en contenir une sans que personne l'ait voulu.
func (k AccessKey) MarshalJSON() ([]byte, error) {
	return []byte(`"` + keyMask + `"`), nil
}

// KeyDescriptor est ce qu'on peut dire d'une clé sans la révéler.
//
// C'est ce que l'API d'administration rend et ce que `warden key show` affiche :
// de quoi déployer la partie publique sur une flotte et de quoi comparer une
// empreinte, rien de plus.
type KeyDescriptor struct {
	Environment string
	PublicKey   string
	Fingerprint string

	// SecretName pointe le Secret Kubernetes portant la partie privée. Un
	// POINTEUR, jamais la valeur. Il est stocké et non recalculé : le jour où la
	// convention de nommage change, les lignes existantes continuent de désigner
	// le bon Secret.
	SecretName string

	Origin    KeyOrigin
	CreatedAt time.Time
	// RotatedAt est le temps nul tant que la clé n'a jamais été remplacée.
	RotatedAt time.Time
	CreatedBy int64
}

// SecretNameFor compose le nom du Secret portant la partie privée.
//
// Ici et pas dans l'adaptateur : la convention est lue par qui écrit le Secret
// ET par qui le relit, et deux compositions jumelles finissent par diverger —
// c'est la leçon que ce dépôt a déjà payée avec `WARDEN_DISPATCH_URL`.
func SecretNameFor(environment string) string {
	return "warden-key-" + environment
}

// KeyOrigin dit d'où vient la partie privée.
type KeyOrigin string

const (
	// KeyGenerated : warden a frappé la paire, la partie privée n'a jamais
	// quitté le cluster.
	KeyGenerated KeyOrigin = "generated"
	// KeyImported : elle vient d'un opérateur, donc elle a existé ailleurs.
	// La distinction compte le jour où l'on se demande jusqu'où une
	// compromission remonte.
	KeyImported KeyOrigin = "imported"
)

// Valid dit si l'origine fait partie de celles que le schéma accepte.
func (o KeyOrigin) Valid() bool {
	return o == KeyGenerated || o == KeyImported
}

// ErrNoKey signale qu'aucune clé n'est posée pour cet environnement.
//
// Ce n'est PAS une erreur de configuration en soi : un playbook en
// `connection: local` n'a besoin d'aucune clé, et c'est le cas de tous les runs
// du produit jusqu'à aujourd'hui. L'appelant décide.
var ErrNoKey = errors.New("aucune clé d'accès pour cet environnement")

// KeyIssuer répond à « avec quoi joindre ces hôtes en tant que cet
// utilisateur » (ADR-0020).
//
// `Issue` et non `Get` : une CA SSH ne retrouve pas une clé, elle en ÉMET une.
// `user` et `hostScope` ne servent à rien à l'adaptateur Kubernetes — il rend la
// clé de l'environnement — mais ce sont précisément les deux paramètres qui
// rendront l'adaptateur Vault indolore, les principals d'un certificat dérivant
// du périmètre (§10.2).
//
// Les ignorer est une décision LOCALE à l'adaptateur, pas une lacune de cette
// interface : le cœur les passe toujours.
type KeyIssuer interface {
	Issue(ctx context.Context, user, environment string, hostScope []string) (AccessKey, error)
}

// KeyStore pose et lit les clés d'environnement.
//
// Séparé de `KeyIssuer`, et ce n'est pas de la décomposition gratuite : l'un est
// sur le chemin chaud de chaque run et n'a besoin que d'émettre, l'autre sert
// l'administration. Les fondre donnerait au composeur d'ordres le droit de
// remplacer la clé de la production.
type KeyStore interface {
	// Import pose une partie privée fournie par un opérateur.
	//
	// La clé DOIT être sans passphrase, et l'implémentation doit le vérifier.
	// MESURÉ : `ansible-runner` charge la clé dans un `ssh-agent` par
	// `ssh-add` avant de lancer le playbook ; une clé protégée ferait attendre
	// une saisie qui ne viendra jamais, et le run resterait bloqué jusqu'à
	// `activeDeadlineSeconds`.
	Import(ctx context.Context, environment string, private []byte) (KeyDescriptor, error)

	// Generate frappe une paire. La partie privée ne quitte jamais le cluster.
	Generate(ctx context.Context, environment string) (KeyDescriptor, error)

	// Describe rend ce qu'on peut dire d'une clé sans la révéler. Erreur
	// enveloppant ErrNoKey quand l'environnement n'en a pas.
	Describe(ctx context.Context, environment string) (KeyDescriptor, error)
}

// KeyCatalog conserve la partie publique et les métadonnées.
//
// Deux magasins pour une clé, et la séparation est la décision : PostgreSQL ne
// porte JAMAIS la partie privée (migration 00007), le Secret Kubernetes ne porte
// que celle-là. Un seul magasin obligerait à choisir entre un journal d'audit
// qui contient des secrets et une configuration qu'on ne peut pas requêter.
type KeyCatalog interface {
	// SaveKey écrit ou remplace la partie publique et ses métadonnées.
	SaveKey(ctx context.Context, d KeyDescriptor) error
	// KeyByEnvironment rend le descripteur, ou une erreur enveloppant ErrNoKey.
	KeyByEnvironment(ctx context.Context, environment string) (KeyDescriptor, error)
	// ListKeys rend tous les descripteurs, triés par environnement.
	ListKeys(ctx context.Context) ([]KeyDescriptor, error)
}

// ValidateEnvironment applique la contrainte de forme du schéma.
//
// Au bord plutôt qu'au fond : cette valeur devient un composant de nom de Secret
// Kubernetes, et une violation découverte par l'API Kubernetes se présenterait à
// la livraison de l'ordre, très loin d'ici.
func ValidateEnvironment(env string) error {
	if env == "" {
		return errors.New("environnement absent")
	}
	if len(env) > 55 {
		return fmt.Errorf("environnement %q : %d caractères, 55 au plus", env, len(env))
	}
	for i, c := range env {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '-' && i > 0:
		default:
			return fmt.Errorf("environnement %q : seuls [a-z0-9-] sont acceptés, "+
				"cette valeur devient un nom de Secret Kubernetes", env)
		}
	}
	return nil
}

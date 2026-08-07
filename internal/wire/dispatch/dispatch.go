// Package dispatch porte le contrat réseau du canal de contrôle serveur →
// runner (ADR-0018) : forme des messages, encodage SSE, masquage.
//
// Troisième protocole du produit, et délibérément distinct des deux autres.
// `wire/ingest` est le sens montant, `wire/ws` celui de la CLI — que l'ADR-0011
// interdit au pod hostile de lier. Celui-ci descend, et il est le seul à
// transporter des credentials.
//
// Types et (dé)sérialisation uniquement : ni client, ni serveur.
package dispatch

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Kind est le type d'un message de dispatch.
type Kind string

const (
	// KindOrder est l'ordre de run : ce que le pod doit exécuter, et de quoi le
	// faire. C'est le message qui porte des credentials.
	KindOrder Kind = "order"
	// KindCancel est le signal d'annulation du §5.6.
	KindCancel Kind = "cancel"
	// KindHello confirme au pod que son canal est ouvert et qu'il est en
	// attente. Sans lui, un pod chaud ne saurait pas distinguer « connecté,
	// aucun run » de « pas encore connecté », et son propre diagnostic de
	// démarrage deviendrait impossible.
	KindHello Kind = "hello"
)

// ErrProtocol signale un flux mal formé.
var ErrProtocol = errors.New("protocole de dispatch")

// Secret est une valeur sensible transportée par le canal.
//
// Le type existe pour deux raisons, toutes deux dictées par l'ADR-0018.
//
// La première est le masquage : le body des messages de dispatch ne doit
// JAMAIS être journalisé, et un `%v` sur une structure qui en contient est le
// chemin le plus court vers l'inverse.
//
// La seconde est l'encodage, que l'ADR-0018 laisse explicitement en dette :
// « SSE ne transporte que du texte […] il doit être fixé explicitement, pas
// hérité d'un défaut de bibliothèque ». Un `[]byte` est encodé en base64 par
// `encoding/json`, ce qui garantit qu'une clé privée PEM — donc multi-lignes —
// ne peut pas produire de saut de line dans le flux. Or un saut de line dans
// une trame SSE n'est pas une valeur corrompue : c'est une FIN DE MESSAGE.
type Secret []byte

// nolint:gosec
const secretMask = "[valeur sensible supprimée]"

// String masque la valeur.
func (s Secret) String() string { return secretMask }

// GoString masque la valeur sous `%#v`, que String ne couvre pas.
func (s Secret) GoString() string { return secretMask }

// Reveal rend la valeur en clair. Nom délibérément inconfortable : il doit se
// voir en revue.
func (s Secret) Reveal() []byte { return []byte(s) }

// Order est l'ordre de run (ADR-0018).
//
// Il porte les flags EFFECTIFS, après filtrage par la politique serveur
// (§5.4.2) — jamais la demande brute du client. C'est la leçon d'Atlantis : le
// serveur détient la politique, la requête n'est qu'une requête.
//
// Il ne porte PAS de `run_id`. Le runner n'en a pas besoin : le serveur résout
// `pod -> run` depuis le jeton présenté (ADR-0023), aussi bien ici que sur
// l'ingestion. Lui en donner un l'inviterait à s'en servir.
type Order struct {
	// RunRef est le ULID du run, sûr comme nom de ref git (ADR-0007) et dans
	// une URL. C'est ce que le runner affiche et ce qu'il fetch.
	RunRef string `json:"run_ref"`

	// AuditSHA est le commit à exécuter. Ce qui s'exécute correspond
	// exactement au commit journalisé (§8.2).
	AuditSHA string `json:"audit_sha"`

	Environment string `json:"environment"`
	Inventory   string `json:"inventory"`
	Playbook    string `json:"playbook"`

	// Flags effectifs (§5.5), tels qu'ils seront journalisés.
	Flags []string `json:"flags,omitempty"`

	// GitURL est l'origine à cloner, et GitToken le jeton de l'ADR-0007 — dont
	// cette ADR ne disait pas comment il entrait dans le pod. C'est ici.
	GitURL   string `json:"git_url"`
	GitToken Secret `json:"git_token,omitempty"`

	// SSHKey est le credential d'accès émis par `key-inventory` au moment du
	// claim (ADR-0020). Jamais monté, jamais présent avant le claim (§8.3).
	SSHKey Secret `json:"ssh_key,omitempty"`

	// ForwardEnv porte les variables du shell de l'utilisateur retenues par
	// l'allowlist du §5.4.1. Les NOMS ne sont pas sensibles — ils sont déclarés
	// dans le fichier projet, versionné — mais les valeurs le sont : ce sont
	// elles que l'ADR-0017 masque dans les diffs.
	ForwardEnv map[string]Secret `json:"forward_env,omitempty"`
}

// String rend un résumé sans aucune valeur sensible.
//
// Il est utile, et c'est le point : une structure dont la seule impression
// possible serait « [supprimé] » finirait par être décomposée à la main dans un
// log, champ par champ, et c'est là que la fuite arrive.
func (o Order) String() string {
	sha := o.AuditSHA
	if len(sha) > 12 {
		sha = sha[:12]
	}
	return fmt.Sprintf("order(run=%s sha=%s env=%s playbook=%s flags=%d env_vars=%d)",
		o.RunRef, sha, o.Environment, o.Playbook, len(o.Flags), len(o.ForwardEnv))
}

// GoString masque sous `%#v`, que String ne couvre pas.
func (o Order) GoString() string { return o.String() }

// Message est l'enveloppe d'un message de dispatch.
type Message struct {
	Kind  Kind   `json:"-"`
	Order *Order `json:"order,omitempty"`
	// Reason accompagne une annulation : « qui, et pourquoi » est ce que le
	// §6.4 demande d'une terminaison, et un runner qui reçoit un ordre d'arrêt
	// sans motif ne peut rien en journaliser.
	Reason string `json:"reason,omitempty"`
}

// maxMessageBytes borne un message de dispatch.
//
// Le canal descend vers du code hostile (§2), mais la borne sert d'abord dans
// l'autre sens : elle empêche qu'un ordre malformé — une clé lue depuis le
// mauvais fichier, une variable d'environnement démesurée — parte en boucle
// vers tous les pods chauds.
const maxMessageBytes = 512 * 1024

// Encode écrit un message au format SSE.
//
// UNE seule line `data:`, par construction : le body est du JSON compact, et
// les valeurs sensibles sont des `Secret`, donc du base64. C'est la décision que
// l'ADR-0018 laissait en dette — l'encodage n'est hérité d'aucun défaut de
// bibliothèque, il est fixé ici et vérifié par le test.
//
// Le contrôle du saut de line n'est pas de la prudence décorative : une clé
// privée PEM écrite telle quelle dans une trame SSE serait interprétée comme
// une fin de message, et le reste de la clé comme un message suivant.
func Encode(w io.Writer, m Message) error {
	if m.Kind == "" {
		return fmt.Errorf("%w: message sans type", ErrProtocol)
	}
	if strings.ContainsAny(string(m.Kind), "\r\n") {
		return fmt.Errorf("%w: type de message avec saut de ligne", ErrProtocol)
	}

	body, err := json.Marshal(m)
	if err != nil {
		// Jamais le message dans l'erreur : il porte des credentials.
		return fmt.Errorf("%w: sérialisation du message %s", ErrProtocol, m.Kind)
	}
	if len(body) > maxMessageBytes {
		return fmt.Errorf("%w: message %s de %d octets, plafond %d",
			ErrProtocol, m.Kind, len(body), maxMessageBytes)
	}
	// Ceinture et bretelles. `encoding/json` échappe déjà les sauts de ligne
	// dans les chaînes, et les `Secret` sont en base64 : cette vérification ne
	// doit jamais se déclencher. Si elle se déclenche un jour, c'est que
	// l'encodage a changé sous nos pieds — et le mode de défaillance qu'elle
	// empêche est une clé privée coupée en deux au milieu d'un flux.
	if bytes.ContainsAny(body, "\r\n") {
		return fmt.Errorf("%w: le body du message %s contient un saut de ligne", ErrProtocol, m.Kind)
	}

	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", m.Kind, body)
	return err
}

// Decoder relit un flux SSE de dispatch, côté runner.
type Decoder struct {
	sc *bufio.Scanner
}

// NewDecoder construit un lecteur sur le flux.
func NewDecoder(r io.Reader) *Decoder {
	sc := bufio.NewScanner(r)
	// Le plafond du lecteur suit celui de l'écrivain, majoré du préfixe SSE :
	// un ordre légitime au plafond doit pouvoir être relu, sinon la borne
	// devient une panne au lieu d'une protection.
	sc.Buffer(make([]byte, 0, 64*1024), maxMessageBytes+1024)
	return &Decoder{sc: sc}
}

// Next rend le message suivant, ou io.EOF à la fin du flux.
//
// Les commentaires SSE (`:`) et les champs inconnus sont ignorés, comme le veut
// le format : c'est ce qui permettra d'ajouter un type de message sans casser
// les runners déjà déployés.
func (d *Decoder) Next() (Message, error) {
	var kind Kind
	for d.sc.Scan() {
		line := d.sc.Text()

		switch {
		case line == "":
			// Fin de trame sans données utiles : trame de maintien, on continue.
			kind = ""
		case strings.HasPrefix(line, ":"):
			// Commentaire — c'est ainsi qu'on tient une connexion ouverte à
			// travers un proxy sans inventer de message.
		case strings.HasPrefix(line, "event:"):
			kind = Kind(strings.TrimSpace(strings.TrimPrefix(line, "event:")))
		case strings.HasPrefix(line, "data:"):
			body := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if kind == "" {
				return Message{}, fmt.Errorf("%w: données sans type", ErrProtocol)
			}
			var m Message
			if err := json.Unmarshal([]byte(body), &m); err != nil {
				return Message{}, fmt.Errorf("%w: body illisible", ErrProtocol)
			}
			m.Kind = kind
			if m.Kind == KindOrder && m.Order == nil {
				return Message{}, fmt.Errorf("%w: ordre sans contenu", ErrProtocol)
			}
			return m, nil
		}
	}
	if err := d.sc.Err(); err != nil {
		return Message{}, fmt.Errorf("lecture du flux de dispatch: %w", err)
	}
	return Message{}, io.EOF
}

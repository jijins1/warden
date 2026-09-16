package exec

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/jijins1/warden/internal/events"
)

// ClePayloadErreur est la clé du socle, reprise ici pour la lisibilité.
//
// Elle vit dans `internal/events` parce que la CLI doit la LIRE et qu'elle ne
// peut pas importer ce paquet-ci (ADR-0011). Même convention que `warden_flux`,
// déjà émis par `lire` sur un flux tronqué.
const ClePayloadErreur = events.CleErreur

// longueurMaxDiagnostic borne le message rendu visible.
//
// Il finit dans un terminal et dans un navigateur : de quoi lire une erreur
// d'Ansible, pas de quoi y déverser un flux. La convention maison est de borner
// explicitement plutôt que de s'en remettre à une limite de transport — ici
// `MaxBatchBytes`, à 8 Mio, ne dirait rien d'utile.
const longueurMaxDiagnostic = 2000

// marqueurTroncature dit que le message a été coupé.
//
// La troncature est VISIBLE, comme celle de `maxLigne` et contrairement à celle
// de `borne` : un message amputé en silence se lit comme un message complet, et
// mène à chercher la suite d'une phrase qui n'a jamais été envoyée.
const marqueurTroncature = "… (tronqué)"

// sequencesANSI reconnaît ce qu'un terminal INTERPRÈTE au lieu de l'afficher.
//
// Les trois formes qui comptent : CSI (`ESC [ … lettre`), OSC (`ESC ] … BEL` ou
// `ESC ] … ESC \`), et tout autre `ESC` suivi d'un caractère. Retirer l'`ESC`
// seul ne suffirait pas — il laisserait derrière lui un « [0m » bien visible.
var sequencesANSI = regexp.MustCompile(`\x1b(?:\[[0-9;?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\)|[@-Z\\-_])`)

// assainir rend un texte du runner affichable sans danger.
//
// PREMIER assainisseur sortant du dépôt, et il faut dire pourquoi il existe :
// le texte vient d'`ansible-runner`, donc du domaine hostile de l'ADR-0011, et
// il va désormais atteindre un terminal (`warden logs`) et un navigateur. Le
// navigateur est couvert — l'interface n'interpole jamais par `innerHTML` — le
// terminal ne l'était pas du tout.
//
// Il NETTOIE au lieu de refuser, contrairement à `verifierExtraVar` qui est le
// seul contrôle de caractères de contrôle existant : celui-là garde une valeur
// hors du système, celui-ci fait entrer un message de diagnostic. Refuser
// rendrait au silence le run qu'on cherche justement à faire parler.
func assainir(s string) string {
	s = sequencesANSI.ReplaceAllString(s, "")

	// Les caractères de contrôle restants deviennent des espaces plutôt que de
	// disparaître : un retour à la ligne qui s'évapore recolle deux mots.
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == unicode.ReplacementChar:
			// Octet invalide en UTF-8 : le laisser passerait un caractère de
			// remplacement dans un message censé rassurer.
			b.WriteByte(' ')
		case unicode.IsControl(r):
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}

	// Les fragments d'Ansible arrivent découpés et réalignés : recoller sur une
	// seule ligne lisible vaut mieux que de reproduire sa mise en page.
	champs := strings.Fields(b.String())
	propre := strings.Join(champs, " ")

	// En RUNES et non en octets : couper au milieu d'un caractère produirait
	// exactement le remplacement qu'on vient d'écarter.
	if r := []rune(propre); len(r) > longueurMaxDiagnostic {
		propre = string(r[:longueurMaxDiagnostic]) + marqueurTroncature
	}
	return propre
}

// EventErreur compose l'event qui dit POURQUOI un run n'a rien exécuté.
//
// Sans hôte ni tâche, et c'est fidèle : il n'y en a pas eu. C'est précisément
// ce qui le rendait invisible jusqu'ici — l'interface écarte les events sans
// hôte, à juste titre pour le cycle de vie d'Ansible — d'où `ClePayloadErreur`,
// qui distingue celui-ci de ce bruit-là.
//
// `State` vaut `failed` : c'en est un. Le compte de `changed` n'en souffre pas,
// il ne regarde que `changed`.
//
// Le payload est composé par `json.Marshal`, jamais par concaténation : le
// message peut venir d'`ansible-runner`, donc d'un texte hostile, et le coller
// dans un gabarit JSON serait lui donner le droit d'en sortir.
func EventErreur(message string) events.Event {
	propre := assainir(message)
	// Le masquage des secrets n'est PAS fait ici : le rapporteur l'applique au
	// payload de chaque event (ADR-0017), donc celui-ci le traverse comme les
	// autres. Le refaire ici le ferait deux fois, et à un endroit qui ne connaît
	// pas les secrets du run.
	brut, err := json.Marshal(map[string]string{ClePayloadErreur: propre})
	if err != nil {
		// `map[string]string` ne peut pas échouer à l'encodage ; le repli existe
		// pour que cette fonction n'ait pas à rendre d'erreur à un appelant qui
		// est déjà en train d'en signaler une.
		brut = json.RawMessage(`{"` + ClePayloadErreur + `":"diagnostic illisible"}`)
	}
	return events.Event{
		OccurredAt: time.Now().UTC(),
		State:      events.StateFailed,
		Payload:    brut,
	}
}

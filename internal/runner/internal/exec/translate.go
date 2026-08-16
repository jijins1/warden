package exec

import (
	"bytes"
	"encoding/json"
	"time"

	"github.com/jijins1/warden/internal/events"
)

// Noms des events d'`ansible-runner` que le traducteur reconnaît.
//
// MESURÉS sur ansible-runner 2.4.3 / ansible-core 2.18.6, pas lus dans une
// documentation : les fixtures de `testdata` sont la sortie réelle d'un run, et
// c'est contre elles que ce fichier est écrit. La liste est délibérément COURTE
// — tout le reste devient `unknown` avec son payload intact, ce que l'ADR-0021
// impose comme soupape.
const (
	evOK          = "runner_on_ok"
	evFailed      = "runner_on_failed"
	evSkipped     = "runner_on_skipped"
	evUnreachable = "runner_on_unreachable"
	evFileDiff    = "runner_on_file_diff"
	evTaskStart   = "playbook_on_task_start"
)

// trame est la partie d'une ligne du flux dont le traducteur a besoin.
//
// Le reste de la ligne n'est pas décrit ici, et c'est voulu : il part dans le
// payload tel quel. Décrire la totalité de la structure d'`ansible-runner`
// reviendrait à s'engager à la suivre à chaque version, ce que l'ADR-0021
// refuse explicitement.
type trame struct {
	Counter   int64           `json:"counter"`
	Event     string          `json:"event"`
	Created   string          `json:"created"`
	EventData donnees         `json:"event_data"`
	Status    string          `json:"status"`
	Zipfile   *int64          `json:"zipfile"`
	Brut      json.RawMessage `json:"-"`
}

type donnees struct {
	Host     string          `json:"host"`
	Task     string          `json:"task"`
	Role     string          `json:"role"`
	TaskUUID string          `json:"task_uuid"`
	Diff     json.RawMessage `json:"diff"`
	Res      resultat        `json:"res"`
}

type resultat struct {
	Changed bool            `json:"changed"`
	Diff    json.RawMessage `json:"diff"`
}

// estEvent dit si la trame porte un event de run plutôt qu'une trame de
// contrôle.
//
// La distinction n'est pas cosmétique. La trame `status: starting` contient
// `env`, c'est-à-dire TOUT l'environnement du processus — donc, le jour où
// `forward_env` existera, les valeurs que l'ADR-0017 est censée masquer. Elle
// ne doit jamais devenir un payload.
func (t trame) estEvent() bool { return t.Event != "" }

// Traducteur convertit le flux d'`ansible-runner` en enveloppes de l'ADR-0021.
//
// Il tient un état : le numéro de tâche, qu'`ansible-runner` n'émet pas. La
// numérotation vient donc du COMPTE des `playbook_on_task_start`, mémorisé par
// `task_uuid` — et non d'un compteur global lu au vol, parce que la stratégie
// `free` fait progresser plusieurs hôtes sur des tâches différentes en même
// temps.
type Traducteur struct {
	numeros map[string]int
	suivant int
}

// NewTraducteur construit un traducteur.
func NewTraducteur() *Traducteur {
	return &Traducteur{numeros: make(map[string]int)}
}

// Traduire convertit une ligne du flux en event, ou rend false si la ligne n'en
// porte pas.
//
// Elle ne rend AUCUNE erreur, et c'est une contrainte de l'ADR-0021 : le
// traducteur vit dans le domaine hostile et doit être incapable de faire
// échouer un run. Une ligne illisible devient un event `unknown` qui la
// contient, jamais un arrêt.
func (tr *Traducteur) Traduire(ligne []byte) (events.Event, bool) {
	ligne = bytes.TrimSpace(ligne)
	if len(ligne) == 0 {
		return events.Event{}, false
	}

	var t trame
	if err := json.Unmarshal(ligne, &t); err != nil {
		// Ligne illisible. `-j` d'`ansible-runner` mélange d'ailleurs du texte
		// brut au JSON sur stdout — c'est mesuré — et c'est l'une des raisons
		// pour lesquelles ce paquet lit `worker` et non `run -j`. Si cela
		// arrivait quand même, la ligne est conservée plutôt que jetée.
		return tr.inconnu(ligne), true
	}
	t.Brut = json.RawMessage(ligne)

	if !t.estEvent() {
		return events.Event{}, false
	}
	return tr.event(t), true
}

// Status rend le statut porté par une trame de contrôle, vide sinon.
//
// C'est de LÀ que vient l'issue du run, et non du code de sortie du processus :
// le §6.4 note que « l'exit code du pod dit que le processus est mort, pas ce
// que le playbook a fait ».
func Status(ligne []byte) string {
	var t trame
	if err := json.Unmarshal(bytes.TrimSpace(ligne), &t); err != nil {
		return ""
	}
	if t.estEvent() {
		return ""
	}
	return t.Status
}

// FinDeFlux dit si la trame annonce l'archive d'artefacts qui termine le flux.
//
// MESURÉ : `ansible-runner worker` clôt sa sortie par `{"zipfile": N}` suivi de
// N octets BINAIRES. Continuer à lire au-delà produirait des lignes illisibles
// qui deviendraient autant d'events `unknown`, c'est-à-dire du bruit d'audit
// permanent (ADR-0013).
func FinDeFlux(ligne []byte) bool {
	var t trame
	if err := json.Unmarshal(bytes.TrimSpace(ligne), &t); err != nil {
		return false
	}
	return t.Zipfile != nil
}

func (tr *Traducteur) event(t trame) events.Event {
	if t.Event == evTaskStart && t.EventData.TaskUUID != "" {
		tr.suivant++
		tr.numeros[t.EventData.TaskUUID] = tr.suivant
	}

	e := events.Event{
		OccurredAt: horodatage(t.Created),
		Host:       t.EventData.Host,
		TaskNumber: tr.numeros[t.EventData.TaskUUID],
		TaskName:   t.EventData.Task,
		Role:       t.EventData.Role,
		State:      etat(t),
		Payload:    t.Brut,
	}

	// Le diff est SORTI du payload, pas seulement recopié. Il part dans une
	// table à contrôle d'accès distinct parce qu'il contient les secrets rendus
	// (§6.5) ; le laisser aussi dans le payload rendrait cette séparation
	// décorative — on ouvrirait le journal en croyant ne pas ouvrir les
	// secrets.
	if diff := diffDe(t); diff != nil {
		e.Diff = diff
		e.Payload = sansDiff(t.Brut)
	}
	return e
}

// etat applique le partage de l'ADR-0021.
func etat(t trame) events.State {
	switch t.Event {
	case evOK:
		// `changed` n'est consulté QUE sur un succès. MESURÉ : une commande qui
		// échoue rapporte `changed: true`, et un traducteur qui lirait le
		// drapeau avant le type d'event compterait des échecs comme des
		// modifications — dans la colonne qui est la métrique d'audit
		// principale (§6.5).
		if t.EventData.Res.Changed {
			return events.StateChanged
		}
		return events.StateOK
	case evFailed:
		return events.StateFailed
	case evSkipped:
		return events.StateSkipped
	case evUnreachable:
		return events.StateUnreachable
	default:
		// La soupape de l'ADR-0021 : `playbook_on_start`, `verbose`,
		// `playbook_on_stats`, un module tiers, une version d'ansible-core plus
		// récente que ce fichier. Stocké tel quel, jamais ignoré, jamais perdu.
		return events.StateUnknown
	}
}

// diffDe rend le diff d'une trame, quel que soit l'endroit où il se trouve.
//
// Deux emplacements, tous deux MESURÉS : `event_data.res.diff` sur un
// `runner_on_ok`, et `event_data.diff` sur l'event `runner_on_file_diff` qu'
// Ansible émet EN PLUS, juste avant. Ne traiter que le premier laisserait le
// second déposer le même diff dans la table des events ordinaires.
func diffDe(t trame) json.RawMessage {
	if len(t.EventData.Res.Diff) > 0 && !estNul(t.EventData.Res.Diff) {
		return t.EventData.Res.Diff
	}
	if len(t.EventData.Diff) > 0 && !estNul(t.EventData.Diff) {
		return t.EventData.Diff
	}
	return nil
}

func estNul(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// sansDiff retire les diffs du payload.
//
// C'est le SEUL endroit où le payload cesse d'être la ligne d'origine octet
// pour octet, et l'écart est délibéré : l'ADR-0021 veut le payload intact,
// l'ADR-0008 veut les diffs dans une table séparée, et pour les quelques events
// qui portent un diff les deux ne peuvent pas être vrais en même temps. La
// séparation d'accès l'emporte, parce qu'elle est une propriété de sécurité.
//
// `json.Number` préserve les entiers exactement : sans lui, le ré-encodage
// ferait passer toute valeur numérique par un float64 et réécrirait
// silencieusement, par exemple, la taille d'un fichier.
//
// En cas d'échec, la ligne d'origine est rendue TELLE QUELLE : perdre l'event
// serait pire, et la fonction ne peut pas faire échouer un run.
func sansDiff(brut json.RawMessage) json.RawMessage {
	dec := json.NewDecoder(bytes.NewReader(brut))
	dec.UseNumber()

	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return brut
	}
	ed, ok := m["event_data"].(map[string]any)
	if !ok {
		return brut
	}
	delete(ed, "diff")
	if res, ok := ed["res"].(map[string]any); ok {
		delete(res, "diff")
	}

	out, err := json.Marshal(m)
	if err != nil {
		return brut
	}
	return out
}

// inconnu emballe une ligne illisible sans la perdre.
func (tr *Traducteur) inconnu(ligne []byte) events.Event {
	charge, err := json.Marshal(map[string]string{
		"warden_ligne_illisible": string(ligne),
	})
	if err != nil {
		// La ligne n'est pas encodable en JSON — des octets bruts. On garde le
		// fait qu'elle a existé plutôt que rien.
		charge = json.RawMessage(`{"warden_ligne_illisible":"<non encodable>"}`)
	}
	return events.Event{
		OccurredAt: time.Now().UTC(),
		State:      events.StateUnknown,
		Payload:    charge,
	}
}

// horodatage relit le `created` d'`ansible-runner`.
//
// MESURÉ : RFC 3339 avec décalage explicite, `2026-08-16T15:35:58.122297+00:00`.
// À défaut, l'heure de lecture — un event sans horodatage est refusé par la
// validation, et le perdre pour un champ de forme inattendue serait absurde.
func horodatage(s string) time.Time {
	if s == "" {
		return time.Now().UTC()
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Now().UTC()
	}
	return t.UTC()
}

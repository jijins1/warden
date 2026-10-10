package render

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/jijins1/warden/internal/cli/internal/stream"
	"github.com/jijins1/warden/internal/events"
	"github.com/jijins1/warden/internal/wire/ws"
)

// Issues d'un run, telles que la trame de fin les nomme (§6.4).
//
// Des littéraux, et non les constantes du cœur : la CLI ne peut pas importer
// `internal/server/internal/core` (ADR-0011), et `internal/wire/ingest` lui est
// fermé par depguard — c'est un contrat runner → serveur. Le raisonnement est
// celui que `wire/ingest` tient déjà pour lui-même : le contrat de fil est la
// frontière, et chaque domaine y répète le vocabulaire au lieu de l'emprunter.
const (
	IssueReussi     = "succeeded"
	IssueEchec      = "failed"
	IssueInterrompu = "interrupted"
)

// Suiveur est ce que l'affichage attend du flux.
//
// Déclarée ICI, chez l'appelant, comme `admin.Suiveur` : c'est ce qui permet de
// tester l'affichage sans WebSocket ni serveur.
type Suiveur interface {
	Suivre(ctx context.Context, runRef string, voir func(ws.EventFrame)) (stream.Fin, error)
}

// Run affiche le déroulement d'un run jusqu'à sa fin.
//
// Ce qui s'affiche est le texte qu'ANSIBLE a rendu, tel quel : chaque event
// d'`ansible-runner` porte dans son `stdout` la ligne que le callback par défaut
// aurait écrite — bannières PLAY et TASK, « ok: [hôte] », « fatal: … », PLAY
// RECAP — et ses couleurs, forcées côté runner. Le recomposer serait diverger :
// le format d'Ansible est celui que l'ingénieur sait lire en incident, et une
// imitation finit toujours par en différer sur le cas qu'on n'avait pas prévu.
//
// `couleur` dit si la sortie les garde ; voir `Couleur`.
func Run(ctx context.Context, suiveur Suiveur, runRef string, sortie io.Writer, couleur bool) (stream.Fin, error) {
	r := &rendu{sortie: sortie, couleur: couleur, compte: map[string]*stats{}}

	fin, err := suiveur.Suivre(ctx, runRef, r.event)
	// Le bilan est écrit MÊME sur erreur : une coupure après huit minutes de
	// run ne doit pas emporter ce qu'on a déjà vu passer.
	r.recapituler()
	if err != nil {
		return stream.Fin{}, err
	}
	return fin, nil
}

type rendu struct {
	sortie  io.Writer
	couleur bool

	// recapVu dit qu'Ansible a écrit son PLAY RECAP : la CLI n'en reconstitue
	// alors pas un second.
	recapVu bool

	// ansibleAParle dit qu'au moins un texte rendu par Ansible a été affiché.
	ansibleAParle bool

	// compte ne sert qu'au bilan de REPLI, quand le run s'arrête avant
	// qu'Ansible ait écrit le sien.
	compte map[string]*stats
}

// stats reprend les compteurs d'`AggregateStats` qu'un event permet de
// déduire. `rescued` et `ignored` n'en font pas partie : l'event ne dit pas
// qu'un échec a été rattrapé ou ignoré, et ils restent à zéro dans le repli.
type stats struct {
	ok, changed, unreachable, failed, skipped int
}

// trameAnsible est ce que la CLI lit du payload : le type d'event et le texte
// rendu.
type trameAnsible struct {
	Event  string `json:"event"`
	Stdout string `json:"stdout"`
}

// evStats est l'event qui porte le PLAY RECAP d'Ansible.
const evStats = "playbook_on_stats"

func (r *rendu) event(e ws.EventFrame) {
	// Le DIAGNOSTIC est écrit par warden, pas par Ansible : un run qui échoue
	// sans qu'aucune tâche ne démarre — playbook introuvable — n'a aucun texte
	// d'Ansible à montrer. Il prend la forme des erreurs fatales d'Ansible.
	//
	// Il se tait quand Ansible a parlé : sur une erreur de syntaxe, il n'est que
	// la concaténation des trames `verbose`, déjà affichées telles qu'Ansible
	// les a écrites.
	if msg := events.Diagnostic(e.Payload); msg != "" {
		if !r.ansibleAParle {
			r.ecrire(stringc("ERROR! "+msg, couleurErreur))
		}
		return
	}

	var t trameAnsible
	// Un payload illisible n'empêche pas d'afficher la suite : le repli
	// ci-dessous s'appuie sur l'enveloppe, qui reste lisible.
	_ = json.Unmarshal(e.Payload, &t)
	if t.Event == evStats {
		r.recapVu = true
	}
	r.compter(e)

	texte := t.Stdout
	if texte == "" {
		texte = ligneDEtat(e)
	} else {
		r.ansibleAParle = true
	}
	if texte != "" {
		r.ecrire(texte)
	}
}

// ecrire pose un texte d'Ansible sur la sortie.
//
// `ansible-runner` sépare les lignes par `\r\n` — celles d'un pseudo-terminal —
// et n'écrit pas la fin de la dernière : c'est ce que le display d'Ansible
// ajoute en l'imprimant.
func (r *rendu) ecrire(texte string) {
	texte = assainir(strings.ReplaceAll(texte, "\r\n", "\n"))
	if !r.couleur {
		texte = sansCouleur(texte)
	}
	_, _ = fmt.Fprintln(r.sortie, texte)
}

func (r *rendu) compter(e ws.EventFrame) {
	if e.Host == "" {
		return
	}
	s := r.compte[e.Host]
	if s == nil {
		s = &stats{}
		r.compte[e.Host] = s
	}
	// Comme `AggregateStats` : une tâche `changed` compte AUSSI dans `ok`.
	switch e.State {
	case events.StateOK:
		s.ok++
	case events.StateChanged:
		s.ok++
		s.changed++
	case events.StateFailed:
		s.failed++
	case events.StateUnreachable:
		s.unreachable++
	case events.StateSkipped:
		s.skipped++
	}
}

// ligneDEtat reconstitue la ligne du callback par défaut d'Ansible pour un
// event qui n'a pas de texte rendu.
//
// C'est le cas d'un event porteur de diff : le runner en retire le texte, parce
// que ce texte EST le diff, qui ne quitte pas sa table (§6.5). Un event sans
// hôte ni état connu — cycle de vie, diff — ne produit rien, comme chez Ansible.
func ligneDEtat(e ws.EventFrame) string {
	if e.Host == "" {
		return ""
	}
	h := "[" + e.Host + "]"
	switch e.State {
	case events.StateOK:
		return stringc("ok: "+h, couleurOK)
	case events.StateChanged:
		return stringc("changed: "+h, couleurChanged)
	case events.StateSkipped:
		return stringc("skipping: "+h, couleurSkip)
	case events.StateFailed:
		return stringc("fatal: "+h+": FAILED!", couleurErreur)
	case events.StateUnreachable:
		return stringc("fatal: "+h+": UNREACHABLE!", couleurUnreachable)
	default:
		return ""
	}
}

// largeurBanniere est la largeur d'Ansible hors terminal (`Display.columns`) —
// celle du pod, donc celle des bannières qu'Ansible a lui-même rendues.
const largeurBanniere = 79

// recapituler écrit un PLAY RECAP de repli, au format d'Ansible, quand Ansible
// n'a pas pu écrire le sien — coupure du flux, run interrompu.
//
// Trié, comme celui d'Ansible.
func (r *rendu) recapituler() {
	if r.recapVu || len(r.compte) == 0 {
		return
	}

	hotes := make([]string, 0, len(r.compte))
	for h := range r.compte {
		hotes = append(hotes, h)
	}
	sort.Strings(hotes)

	titre := "PLAY RECAP"
	lignes := []string{"\n" + titre + " " + strings.Repeat("*", largeurBanniere-len(titre))}
	for _, h := range hotes {
		s := r.compte[h]
		lignes = append(lignes, fmt.Sprintf("%s : %s %s %s %s %s %s %s",
			couleurDHote(h, s),
			colorize("ok", s.ok, couleurOK),
			colorize("changed", s.changed, couleurChanged),
			colorize("unreachable", s.unreachable, couleurUnreachable),
			colorize("failed", s.failed, couleurErreur),
			colorize("skipped", s.skipped, couleurSkip),
			colorize("rescued", 0, couleurOK),
			colorize("ignored", 0, couleurWarn),
		))
	}
	r.ecrire(strings.Join(lignes, "\n"))
}

// couleurDHote reprend le `hostcolor` d'Ansible : l'hôte en rouge s'il a
// échoué, en jaune s'il a changé, en vert sinon, sur 26 colonnes visibles.
func couleurDHote(h string, s *stats) string {
	code := couleurOK
	switch {
	case s.failed > 0 || s.unreachable > 0:
		code = couleurErreur
	case s.changed > 0:
		code = couleurChanged
	}
	return stringc(h, code) + strings.Repeat(" ", max(0, 26-len(h)))
}

// colorize reprend le `colorize` d'Ansible : un compteur nul reste sans couleur.
func colorize(nom string, n int, code string) string {
	s := fmt.Sprintf("%s=%-4d", nom, n)
	if n == 0 {
		return s
	}
	return stringc(s, code)
}

// Issue écrit la fin du run et dit si elle est un échec.
//
// Séparée de `Run` parce que l'appelant décide ce qu'il fait d'un échec — la
// CLI en tire un code de sortie, un test s'en contente comme valeur.
func Issue(sortie io.Writer, runRef string, fin stream.Fin) bool {
	switch fin.Status {
	case IssueReussi:
		_, _ = fmt.Fprintf(sortie, "\nrun %s : réussi\n", runRef)
		return false
	case IssueInterrompu:
		// La cause est TOUJOURS renseignée sur un run interrompu (§6.4) :
		// « interrompu » sans elle est presque inutile.
		_, _ = fmt.Fprintf(sortie, "\nrun %s : interrompu (%s)\n", runRef, fin.Reason)
		return true
	default:
		if fin.Reason != "" {
			_, _ = fmt.Fprintf(sortie, "\nrun %s : %s (%s)\n", runRef, fin.Status, fin.Reason)
			return true
		}
		_, _ = fmt.Fprintf(sortie, "\nrun %s : %s\n", runRef, fin.Status)
		return true
	}
}

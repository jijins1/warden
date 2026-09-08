package render

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

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
// Le regroupement PAR TÂCHE reprend la forme d'`ansible-playbook`, et ce n'est
// pas de la nostalgie : sur une flotte, la question posée est « où en est cette
// tâche » et non « qu'a fait cette machine ». Une ligne par event, à plat, la
// rendrait illisible dès la troisième machine.
func Run(ctx context.Context, suiveur Suiveur, runRef string, sortie io.Writer) (stream.Fin, error) {
	r := &rendu{sortie: sortie, tache: -1, compte: map[string]map[events.State]int{}}

	fin, err := suiveur.Suivre(ctx, runRef, r.event)
	if err != nil {
		// Le récapitulatif est écrit MÊME sur erreur : une coupure après huit
		// minutes de run ne doit pas emporter ce qu'on a déjà vu passer.
		r.recapituler()
		return stream.Fin{}, err
	}
	r.recapituler()
	return fin, nil
}

type rendu struct {
	sortie io.Writer

	// tache retient le numéro de la dernière tâche annoncée, pour n'écrire son
	// titre qu'une fois. -1 parce que 0 est un numéro de tâche valide.
	tache int

	compte map[string]map[events.State]int
}

func (r *rendu) event(e ws.EventFrame) {
	if e.TaskNumber != r.tache {
		r.tache = e.TaskNumber
		if titre := titreDeTache(e); titre != "" {
			_, _ = fmt.Fprintf(r.sortie, "\n%s\n", titre)
		}
	}

	hote := e.Host
	if hote == "" {
		// Un event sans hôte vient du play lui-même, pas d'une machine.
		hote = "—"
	}
	_, _ = fmt.Fprintf(r.sortie, "  %s %s\n", marqueur(e.State), hote)

	if r.compte[hote] == nil {
		r.compte[hote] = map[events.State]int{}
	}
	r.compte[hote][e.State]++
}

// titreDeTache compose l'en-tête d'une tâche.
//
// Le rôle préfixe le nom quand il existe, comme Ansible l'affiche : sans lui,
// deux rôles portant une tâche « installer le paquet » deviennent
// indiscernables au moment précis où l'on cherche laquelle a échoué.
func titreDeTache(e ws.EventFrame) string {
	switch {
	case e.Role != "" && e.TaskName != "":
		return "TASK [" + e.Role + " : " + e.TaskName + "]"
	case e.TaskName != "":
		return "TASK [" + e.TaskName + "]"
	default:
		return ""
	}
}

// marqueur rend l'état d'un event en un caractère.
//
// `unreachable` a son propre signe et ne se confond pas avec `failed` : une
// machine injoignable est un problème de RÉSEAU ou de clé, pas de playbook, et
// les deux ne se corrigent pas au même endroit.
func marqueur(etat events.State) string {
	switch etat {
	case events.StateFailed:
		return "✗"
	case events.StateUnreachable:
		return "⚠"
	case events.StateChanged:
		return "+"
	case events.StateSkipped:
		return "-"
	default:
		return "·"
	}
}

// ordreRecap fige les colonnes du récapitulatif.
//
// `changed` avant `ok` : c'est la métrique d'audit principale du produit
// (ADR-0008), et la mettre en tête est ce qui la fait lire.
var ordreRecap = []events.State{
	events.StateChanged, events.StateOK, events.StateFailed,
	events.StateUnreachable, events.StateSkipped,
}

// recapituler écrit le bilan par machine, à la façon du PLAY RECAP d'Ansible.
//
// Trié : l'ordre d'itération d'une map varie d'un run à l'autre, et un
// récapitulatif dont les lignes bougent sans que rien n'ait changé se relit mal.
func (r *rendu) recapituler() {
	if len(r.compte) == 0 {
		return
	}

	hotes := make([]string, 0, len(r.compte))
	for h := range r.compte {
		hotes = append(hotes, h)
	}
	sort.Strings(hotes)

	_, _ = fmt.Fprintf(r.sortie, "\nBilan\n")
	tw := tabwriter.NewWriter(r.sortie, 0, 0, 2, ' ', 0)
	for _, h := range hotes {
		colonnes := make([]string, 0, len(ordreRecap))
		for _, etat := range ordreRecap {
			if n := r.compte[h][etat]; n > 0 {
				colonnes = append(colonnes, fmt.Sprintf("%s=%d", etat, n))
			}
		}
		_, _ = fmt.Fprintf(tw, "  %s\t%s\n", h, strings.Join(colonnes, "  "))
	}
	_ = tw.Flush()
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

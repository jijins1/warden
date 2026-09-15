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
//
// Chaque tâche tient sur UNE ligne de résumé (« ok=3 changed=1 ») plutôt qu'une
// ligne par hôte : sur une flotte, la question posée est « combien ont changé »,
// pas le détail machine par machine tant que rien n'a échoué. Les hôtes en
// échec ou injoignables restent nommés juste en dessous, sans repli possible :
// c'est l'information qu'on ne doit jamais avoir à aller chercher.
func Run(ctx context.Context, suiveur Suiveur, runRef string, sortie io.Writer) (stream.Fin, error) {
	r := &rendu{sortie: sortie, tache: -1, compte: map[string]map[events.State]int{}}

	fin, err := suiveur.Suivre(ctx, runRef, r.event)
	if err != nil {
		// Le récapitulatif est écrit MÊME sur erreur : une coupure après huit
		// minutes de run ne doit pas emporter ce qu'on a déjà vu passer.
		r.clorreTache()
		r.recapituler()
		return stream.Fin{}, err
	}
	r.clorreTache()
	r.recapituler()
	return fin, nil
}

type rendu struct {
	sortie io.Writer

	// tache retient le numéro de la dernière tâche annoncée, pour n'écrire son
	// titre qu'une fois. -1 parce que 0 est un numéro de tâche valide.
	tache int

	// tacheCompte et tacheEchecs portent le résumé de la tâche EN COURS,
	// vidés dans clorreTache() dès que la tâche suivante commence.
	tacheCompte map[events.State]int
	tacheEchecs []echecHote

	compte map[string]map[events.State]int
}

type echecHote struct {
	hote string
	etat events.State
}

func (r *rendu) event(e ws.EventFrame) {
	if e.TaskNumber != r.tache {
		r.clorreTache()
		r.tache = e.TaskNumber
		if titre := titreDeTache(e); titre != "" {
			_, _ = fmt.Fprintf(r.sortie, "\n%s\n", titre)
		}
	}

	// Le DIAGNOSTIC passe avant le filtre ci-dessous, et c'est tout son objet :
	// un run qui échoue sans qu'aucune tâche ne démarre — playbook introuvable,
	// erreur de syntaxe — ne produit que des events sans hôte. Ils étaient donc
	// tous écartés, et `warden logs` n'avait plus qu'un `failed` nu à imprimer.
	//
	// Seule la clé que WARDEN écrit passe : les trames `verbose` d'Ansible
	// restent du bruit, y compris sur un run qui réussit.
	if msg := events.Diagnostic(e.Payload); msg != "" {
		r.clorreTache()
		_, _ = fmt.Fprintf(r.sortie, "\n%s %s\n", marqueur(events.StateFailed), msg)
		return
	}

	hote := e.Host
	if hote == "" {
		// Un event sans hôte vient du play lui-même, pas d'une machine : rien
		// à compter par tâche ni par hôte, et l'afficher ne serait que du
		// bruit — une ligne vide répétée à chaque event de cycle de vie.
		return
	}

	if r.tacheCompte == nil {
		r.tacheCompte = map[events.State]int{}
	}
	r.tacheCompte[e.State]++
	if e.State == events.StateFailed || e.State == events.StateUnreachable {
		r.tacheEchecs = append(r.tacheEchecs, echecHote{hote: hote, etat: e.State})
	}

	if r.compte[hote] == nil {
		r.compte[hote] = map[events.State]int{}
	}
	r.compte[hote][e.State]++
}

// clorreTache écrit le résumé de la tâche en cours, et le vide.
//
// Appelée au changement de tâche ET en fin de run : une tâche dont le run
// s'arrête au milieu doit quand même laisser son résumé, pour la même raison
// que le bilan final survit à une coupure.
func (r *rendu) clorreTache() {
	if len(r.tacheCompte) == 0 {
		return
	}

	colonnes := make([]string, 0, len(ordreRecap))
	for _, etat := range ordreRecap {
		if n := r.tacheCompte[etat]; n > 0 {
			colonnes = append(colonnes, fmt.Sprintf("%s=%d", etat, n))
		}
	}
	_, _ = fmt.Fprintf(r.sortie, "  %s\n", strings.Join(colonnes, "  "))

	for _, ech := range r.tacheEchecs {
		_, _ = fmt.Fprintf(r.sortie, "    %s %s\n", marqueur(ech.etat), ech.hote)
	}

	r.tacheCompte = nil
	r.tacheEchecs = nil
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

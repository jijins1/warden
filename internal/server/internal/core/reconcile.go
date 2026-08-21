package core

import (
	"context"
	"errors"
	"time"
)

// errReconcilerIncomplet : sans les deux ports, le réconciliateur ne peut ni
// lire ce que la base croit, ni ce que le cluster montre.
var errReconcilerIncomplet = errors.New("réconciliateur : lecture des runs et observation des pods sont requises")

// PodFacts est ce que le cluster dit d'un pod, en termes bruts.
//
// Des FAITS et non une cause : l'extraction depuis un objet Kubernetes est le
// travail de l'adaptateur, la traduction en cause de terminaison est du domaine
// (§6.4). Les séparer est ce qui rend la seconde testable sans cluster, et elle
// est la moitié qui compte — une cause fausse dans un journal d'audit est pire
// qu'une cause absente.
type PodFacts struct {
	Name string

	// Termine dit que le pod ne tourne plus. Un pod qui tourne encore n'est pas
	// à réconcilier : son run est peut-être simplement long.
	Termine bool

	// PodReason est `pod.status.reason` : « Evicted », « NodeLost »,
	// « DeadlineExceeded ». Il porte ce qui est arrivé au POD.
	PodReason string

	// ContainerReason est la raison de terminaison du conteneur :
	// « OOMKilled », « Error », « Completed ». Il porte ce qui est arrivé au
	// PROCESSUS.
	ContainerReason string

	// ExitCode du conteneur. Zéro ne veut PAS dire que le run a réussi : il dit
	// que le processus est mort proprement, ce qui n'est pas la même chose
	// (§6.4).
	ExitCode int32
}

// CauseDeTerminaison traduit ce que le cluster a observé en cause du §6.4.
//
// L'ordre des cas n'est pas indifférent : une éviction se manifeste AUSSI par
// un conteneur terminé, et un dépassement de délai par un code de sortie. Le
// fait le plus SPÉCIFIQUE l'emporte, sans quoi tout deviendrait « unknown ».
//
// `CauseUnknown` est délibérément conservée et fréquente. Le §6.4 le dit :
// « ce n'est pas une lacune de conception, c'est un aveu honnête — la supprimer
// par principe conduirait à inventer une cause plausible, bien pire en
// post-mortem ». L'objectif est de la rendre rare, pas de l'éliminer.
func CauseDeTerminaison(f PodFacts) TerminationCause {
	switch {
	case f.PodReason == RaisonEvicted:
		return CausePodEvicted

	case f.PodReason == RaisonNodeLost:
		return CauseNodeLost

	case f.ContainerReason == RaisonOOMKilled:
		return CausePodOOMKilled

	case f.PodReason == RaisonDeadlineExceeded:
		// `activeDeadlineSeconds`, posé par l'orchestrateur, borne un run qui
		// pend même si le contrôleur est mort (décision actée n°13). C'est
		// exactement le « server_task_timeout » du §6.4.
		return CauseServerTaskTimeout

	default:
		// Le conteneur s'est terminé — proprement ou non — sans que le runner
		// ait clos son run. Le processus est mort avant de pouvoir rapporter,
		// et rien de ce qu'on observe ne dit pourquoi.
		return CauseUnknown
	}
}

// Les raisons telles que Kubernetes les écrit.
//
// Des constantes plutôt que des littéraux répétés : ce sont des chaînes
// produites par un composant qu'on ne contrôle pas, et les rassembler rend
// visible d'un coup d'œil ce sur quoi le produit s'appuie.
const (
	RaisonEvicted          = "Evicted"
	RaisonNodeLost         = "NodeLost"
	RaisonOOMKilled        = "OOMKilled"
	RaisonDeadlineExceeded = "DeadlineExceeded"
)

// RunEnCours est un run que la base croit en vol.
type RunEnCours struct {
	ID      int64
	RunRef  string
	PodName string

	// StartedAt sert la période de grâce : un run qui vient de démarrer n'a
	// peut-être pas encore de pod VISIBLE, la création et la liste n'étant pas
	// instantanément cohérentes.
	StartedAt time.Time
}

// RunsEnVol lit les runs que la base croit en cours.
type RunsEnVol interface {
	// RunsEnCours rend les runs à l'état `running`, avec le pod qui les porte.
	RunsEnCours(ctx context.Context) ([]RunEnCours, error)

	// CloseRunSiOuvert clôt un run SANS écraser une clôture existante.
	//
	// La garde est ce qui rend le réconciliateur sûr : le runner clôt son
	// propre run par l'ingestion (§6.4, marche 2), et une clôture non gardée
	// remplacerait « succeeded » par « interrupted / pod_evicted » sur un run
	// qui s'est parfaitement terminé — la course étant d'autant plus probable
	// que le pod se termine juste après avoir rapporté.
	//
	// Rend `true` si la clôture a eu lieu.
	CloseRunSiOuvert(ctx context.Context, runID int64, cause TerminationCause) (bool, error)
}

// PodsObserves lit et supprime les pods runner.
type PodsObserves interface {
	// ObserverPods rend les faits sur tous les pods runner du namespace,
	// indexés par nom.
	ObserverPods(ctx context.Context) (map[string]PodFacts, error)

	// DeletePod supprime un pod. Idempotent.
	DeletePod(ctx context.Context, name string) error
}

// GraceReconciliation est le délai avant qu'un run soit réconciliable.
//
// Il ne protège pas d'une course sur `MarkRunning` — celui-ci n'est appelé
// qu'après la création du pod, donc un run `running` a forcément eu un pod. Il
// protège de la LISTE : un pod créé à l'instant peut ne pas encore apparaître,
// et le conclure absent clôturerait un run parfaitement vivant.
const GraceReconciliation = 30 * time.Second

// Reconciler ramène l'état de la base sur ce que le cluster montre (§8.7).
//
// C'est la marche 3 de la machine à états du §6.4 : « un watch informer sur les
// pods récupère containerStatuses[].lastState.terminated ». Sans elle, un run
// dont le pod meurt brutalement reste `running` pour toujours — et le produit
// ment sur ce qu'il a fait.
//
// Toutes ses actions sont IDEMPOTENTES : la leader election par Lease ne
// garantit pas l'unicité, et un contrôleur qui a perdu son bail sans le savoir
// peut agir en double.
type Reconciler struct {
	runs  RunsEnVol
	pods  PodsObserves
	grace time.Duration

	// maintenant est injectable pour que la période de grâce soit testable sans
	// attendre.
	maintenant func() time.Time
}

// NewReconciler construit le réconciliateur et échoue à froid.
func NewReconciler(runs RunsEnVol, pods PodsObserves) (*Reconciler, error) {
	if runs == nil || pods == nil {
		return nil, errReconcilerIncomplet
	}
	return &Reconciler{
		runs: runs, pods: pods,
		grace:      GraceReconciliation,
		maintenant: time.Now,
	}, nil
}

// IntervalleReconciliation est le rythme du passage.
//
// Il borne le RETARD d'audit, pas le retard d'exécution : un run tué à l'instant
// t est clos au plus tard t+30s, et rien d'autre n'en dépend. Plus court
// coûterait un list au serveur d'API pour rien ; beaucoup plus long laisserait
// une consultation d'audit voir « en cours » un run mort depuis longtemps.
const IntervalleReconciliation = 30 * time.Second

// Boucler réconcilie jusqu'à l'annulation du contexte.
//
// Une erreur de passage n'ARRÊTE PAS la boucle : la base ou l'API Kubernetes
// peuvent être indisponibles un moment, et s'arrêter là laisserait les runs en
// vol jusqu'au prochain redémarrage — c'est-à-dire exactement la panne que ce
// composant existe pour réparer. Elle est rendue à l'appelant qui journalise.
//
// Un passage IMMÉDIAT au démarrage, avant le premier tick : le cas qui compte le
// plus est justement le redémarrage du contrôleur après un incident, où des runs
// attendent déjà d'être clos.
func (r *Reconciler) Boucler(ctx context.Context, intervalle time.Duration, apres func(Bilan, error)) error {
	if intervalle <= 0 {
		intervalle = IntervalleReconciliation
	}
	t := time.NewTicker(intervalle)
	defer t.Stop()

	for {
		bilan, err := r.Reconcile(ctx)
		if apres != nil {
			apres(bilan, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

// Bilan résume un passage, pour la journalisation et les tests.
type Bilan struct {
	Examines int
	Clos     int
	Ramasses int
}

// Reconcile fait un passage complet.
//
// Une erreur sur un run n'interrompt PAS le passage : les runs sont
// indépendants, et abandonner sur le premier laisserait tous les suivants en
// vol. La première erreur est rendue, après que tout le reste a été traité.
func (r *Reconciler) Reconcile(ctx context.Context) (Bilan, error) {
	var bilan Bilan

	encours, err := r.runs.RunsEnCours(ctx)
	if err != nil {
		return bilan, err
	}
	faits, err := r.pods.ObserverPods(ctx)
	if err != nil {
		return bilan, err
	}

	var premiere error
	limite := r.maintenant().Add(-r.grace)

	for _, run := range encours {
		if run.StartedAt.After(limite) {
			// Trop jeune : voir GraceReconciliation.
			continue
		}
		bilan.Examines++

		cause, aClore := r.decider(run, faits)
		if !aClore {
			continue
		}
		clos, err := r.runs.CloseRunSiOuvert(ctx, run.ID, cause)
		if err != nil {
			if premiere == nil {
				premiere = err
			}
			continue
		}
		if clos {
			bilan.Clos++
		}
	}

	ramasses, err := r.ramasser(ctx, encours, faits)
	bilan.Ramasses = ramasses
	if err != nil && premiere == nil {
		premiere = err
	}
	return bilan, premiere
}

// decider dit s'il faut clore un run, et pourquoi.
func (r *Reconciler) decider(run RunEnCours, faits map[string]PodFacts) (TerminationCause, bool) {
	if run.PodName == "" {
		// Un run `running` sans nom de pod : `MarkRunning` a été appelé sans
		// pod, ce qui ne devrait pas arriver. On ne peut rien observer, donc
		// rien affirmer.
		return CauseUnknown, true
	}

	f, present := faits[run.PodName]
	switch {
	case !present:
		// Le pod a existé — `MarkRunning` suit sa création — et il n'est plus
		// là. Supprimé par quelqu'un, ou ramassé par le nœud. Rien de ce qu'on
		// observe ne dit lequel, et `admin_killed` supposerait un acteur qu'on
		// ne connaît pas (§6.4 : « reste à dire qui, et pourquoi »).
		return CauseUnknown, true

	case !f.Termine:
		// Le pod tourne. Le run est peut-être simplement long.
		return "", false

	default:
		return CauseDeTerminaison(f), true
	}
}

// ramasser supprime les pods dont plus aucun run n'a besoin (§8.7).
//
// Un pod n'est JAMAIS réutilisé (§8.2) : une fois terminé et son run clos, il
// n'a plus d'usage. Sans ce ramassage ils s'accumulent dans le namespace —
// c'est ce qu'on observe aujourd'hui.
//
// Ne touche QUE des pods terminés, et jamais un pod dont un run est encore en
// vol : supprimer un pod vivant tuerait un run en cours d'exécution.
func (r *Reconciler) ramasser(ctx context.Context, encours []RunEnCours, faits map[string]PodFacts) (int, error) {
	envol := make(map[string]struct{}, len(encours))
	for _, run := range encours {
		envol[run.PodName] = struct{}{}
	}

	var n int
	var premiere error
	for nom, f := range faits {
		if !f.Termine {
			continue
		}
		if _, encore := envol[nom]; encore {
			// Son run vient peut-être d'être clos dans ce même passage, mais on
			// ne le ramasse qu'au suivant : deux écritures sur le même objet
			// dans un passage rendent le bilan illisible pour rien.
			continue
		}
		if err := r.pods.DeletePod(ctx, nom); err != nil {
			if premiere == nil {
				premiere = err
			}
			continue
		}
		n++
	}
	return n, premiere
}

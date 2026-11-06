package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"time"
)

// Planification est un run planifié (ADR-0057) : un playbook de la principale,
// lancé à heure fixe AU NOM de son propriétaire.
type Planification struct {
	ID  int64
	Nom string

	Repo        string
	Playbook    string
	Environment string
	Inventory   string
	// Flags tels qu'ils seront demandés, `-e` compris. Leurs valeurs sont
	// auditées en clair dans `run.flags` (ADR-0039) : les garder ici ne change
	// rien à leur exposition.
	Flags []string

	Expression string
	Fuseau     string

	// ProprietaireID est l'identité AUTHENTIFIÉE qui a créé le cron, jamais un
	// champ de la requête. Le run part à son nom, sous ses droits du moment.
	ProprietaireID    int64
	ProprietaireSujet string
	Active            bool
	CreeLe            time.Time
	ProchainDeclenche time.Time // zéro quand le cron est désactivé
	DernierDeclenche  time.Time // zéro tant qu'il n'a jamais tiré
	DernierRunRef     string
	DerniereErreur    string
}

// NouvellePlanification est ce que le magasin insère.
type NouvellePlanification struct {
	Nom                                    string
	Repo, Playbook, Environment, Inventory string
	Flags                                  []string
	Expression, Fuseau                     string
	ProprietaireID                         int64
	ProchainDeclenche                      time.Time
}

// Declenchement est ce que le planificateur consigne après un tour.
type Declenchement struct {
	Quand  time.Time
	RunRef string // vide quand aucun run n'a été créé
	Erreur string // vide quand le run est parti
}

// PlanificationStore lit et écrit les crons.
type PlanificationStore interface {
	CreerPlanification(ctx context.Context, n NouvellePlanification) (Planification, error)

	// ListerPlanifications rend les crons non supprimés ; ceux d'un seul
	// propriétaire quand `proprietaireID` est non nul.
	ListerPlanifications(ctx context.Context, proprietaireID int64) ([]Planification, error)

	// PlanificationParID rend ErrNotFound pour un cron supprimé.
	PlanificationParID(ctx context.Context, id int64) (Planification, error)

	// BasculerPlanification active (avec son prochain déclenchement) ou
	// désactive un cron.
	BasculerPlanification(ctx context.Context, id int64, active bool, prochain time.Time) error

	// SupprimerPlanification marque le cron supprimé. Jamais de DELETE : les
	// runs qu'il a lancés le référencent.
	SupprimerPlanification(ctx context.Context, id int64) error

	// ReclamerEchue réclame UN cron actif échu à `maintenant`, écrit son
	// prochain déclenchement calculé par `prochain`, et le rend AVANT cette
	// écriture — c'est son échéance passée qui intéresse l'appelant. Une
	// échéance zéro désactive le cron. `ok` vaut false s'il n'y a rien d'échu.
	//
	// L'exclusion entre réplicas est l'affaire du magasin (SKIP LOCKED) : deux
	// appels concurrents ne rendent jamais le même cron pour la même échéance.
	ReclamerEchue(ctx context.Context, maintenant time.Time,
		prochain func(Planification) time.Time) (p Planification, ok bool, err error)

	ConsignerDeclenchement(ctx context.Context, id int64, d Declenchement) error
}

// ComptesParID relit un compte par son identifiant interne. Le planificateur
// n'a pas de requête authentifiée : c'est son seul moyen de savoir si le
// propriétaire est désactivé.
type ComptesParID interface {
	UserByID(ctx context.Context, id int64) (User, error)
}

// ErrNomDePlanificationPris : un cron du même propriétaire porte déjà ce nom.
var ErrNomDePlanificationPris = errors.New("un cron porte déjà ce nom")

var nomDePlanification = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// DemandeDePlanification est ce qu'un utilisateur envoie pour créer un cron.
type DemandeDePlanification struct {
	Nom, Repo, Playbook, Environment, Inventory string
	Flags                                       []string
	Expression, Fuseau                          string
}

// Planifications porte les cas d'usage de gestion des crons (ADR-0057).
type Planifications struct {
	Store   PlanificationStore
	Repos   LecteurDeDepot
	Access  EnvironmentGrants
	Horloge func() time.Time
}

func (p Planifications) maintenant() time.Time {
	if p.Horloge != nil {
		return p.Horloge()
	}
	return time.Now()
}

// Creer enregistre un cron au nom de `user`.
//
// Ce qui est vérifié ICI : la forme, l'expression, l'existence du dépôt, et le
// droit VIVANT du créateur sur l'environnement — on ne planifie pas ce qu'on
// ne pourrait pas lancer. Ce qui ne l'est PAS : la configuration du commit,
// qui sera lue au déclenchement sur la principale du moment (ADR-0009).
//
// Le gel (ADR-0044) n'empêche pas la création : geler `prod` arrête les
// déclenchements, il n'interdit pas de préparer celui de la semaine prochaine.
func (p Planifications) Creer(ctx context.Context, user *User, d DemandeDePlanification) (Planification, error) {
	if user == nil || user.Disabled {
		return Planification{}, refus(RefusInterdit, "this account is disabled", nil)
	}
	if p.Store == nil || p.Repos == nil {
		return Planification{}, refus(RefusIndisponible, "schedules are not available on this deployment", nil)
	}
	if !nomDePlanification.MatchString(d.Nom) {
		return Planification{}, refus(RefusForme,
			"name must be 1 to 63 lowercase letters, digits, '.', '_' or '-'", nil)
	}
	if d.Repo == "" {
		return Planification{}, refus(RefusForme, "repo is required", nil)
	}
	if d.Playbook == "" {
		return Planification{}, refus(RefusForme, "playbook is required", nil)
	}
	if d.Environment == "" {
		return Planification{}, refus(RefusForme, "environment is required for a schedule", nil)
	}
	if err := ValidateEnvironment(d.Environment); err != nil {
		return Planification{}, refus(RefusForme, err.Error(), nil)
	}
	if len(d.Flags) > 0 {
		if err := ValiderFlags(d.Flags); err != nil {
			return Planification{}, refus(RefusForme, err.Error(), nil)
		}
	}
	expr, err := AnalyserCron(d.Expression, d.Fuseau)
	if err != nil {
		return Planification{}, refus(RefusForme, err.Error(), nil)
	}

	if err := VerifierLAcces(ctx, p.Access, user.ID, d.Environment); err != nil {
		if errors.Is(err, ErrEnvironnementInterdit) {
			return Planification{}, refus(RefusInterdit, "you have no access to environment "+d.Environment, err)
		}
		return Planification{}, refus(RefusInterne, "could not verify your access", err)
	}

	if _, err := p.Repos.GetRepo(ctx, d.Repo); err != nil {
		if errors.Is(err, ErrNotFound) {
			return Planification{}, refus(RefusIntrouvable, "this repository has no server-side configuration yet", nil)
		}
		return Planification{}, refus(RefusInterne, "could not read repository configuration", err)
	}

	cree, err := p.Store.CreerPlanification(ctx, NouvellePlanification{
		Nom: d.Nom, Repo: d.Repo, Playbook: d.Playbook,
		Environment: d.Environment, Inventory: d.Inventory, Flags: d.Flags,
		Expression: expr.Source(), Fuseau: d.Fuseau,
		ProprietaireID:    user.ID,
		ProchainDeclenche: expr.Suivante(p.maintenant()),
	})
	if err != nil {
		if errors.Is(err, ErrNomDePlanificationPris) {
			return Planification{}, refus(RefusConflit, "you already have a schedule named "+d.Nom, err)
		}
		return Planification{}, refus(RefusInterne, "could not record the schedule", err)
	}
	slog.Info("cron créé",
		slog.Int64("schedule", cree.ID), slog.String("nom", cree.Nom),
		slog.String("repo", cree.Repo), slog.String("environnement", cree.Environment),
		slog.String("expression", cree.Expression), slog.String("fuseau", cree.Fuseau),
		slog.Int64("user", user.ID))
	return cree, nil
}

// Lister rend les crons visibles : les siens, tous pour un administrateur.
func (p Planifications) Lister(ctx context.Context, user *User) ([]Planification, error) {
	if p.Store == nil {
		return nil, refus(RefusIndisponible, "schedules are not available on this deployment", nil)
	}
	var proprietaire int64
	if !user.IsAdmin {
		proprietaire = user.ID
	}
	out, err := p.Store.ListerPlanifications(ctx, proprietaire)
	if err != nil {
		return nil, refus(RefusInterne, "could not list schedules", err)
	}
	return out, nil
}

// aMain rend le cron si `user` peut le gérer : son propriétaire, ou un
// administrateur — c'est le bouton d'arrêt de l'exploitant. Un cron d'autrui
// est INTROUVABLE et non interdit, comme un run d'autrui.
func (p Planifications) aMain(ctx context.Context, user *User, id int64) (Planification, error) {
	if p.Store == nil {
		return Planification{}, refus(RefusIndisponible, "schedules are not available on this deployment", nil)
	}
	s, err := p.Store.PlanificationParID(ctx, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Planification{}, refus(RefusIntrouvable, "no such schedule", nil)
		}
		return Planification{}, refus(RefusInterne, "could not read the schedule", err)
	}
	if s.ProprietaireID != user.ID && !user.IsAdmin {
		return Planification{}, refus(RefusIntrouvable, "no such schedule", nil)
	}
	return s, nil
}

// Basculer active ou désactive un cron.
//
// Réactiver repart de MAINTENANT : les échéances passées pendant la
// désactivation ne sont pas rattrapées.
func (p Planifications) Basculer(ctx context.Context, user *User, id int64, active bool) (Planification, error) {
	s, err := p.aMain(ctx, user, id)
	if err != nil {
		return Planification{}, err
	}
	var prochain time.Time
	if active {
		expr, err := AnalyserCron(s.Expression, s.Fuseau)
		if err != nil {
			return Planification{}, refus(RefusForme, err.Error(), nil)
		}
		prochain = expr.Suivante(p.maintenant())
	}
	if err := p.Store.BasculerPlanification(ctx, id, active, prochain); err != nil {
		return Planification{}, refus(RefusInterne, "could not update the schedule", err)
	}
	slog.Info("cron basculé", slog.Int64("schedule", id), slog.Bool("actif", active),
		slog.Int64("par", user.ID))
	s.Active, s.ProchainDeclenche = active, prochain
	return s, nil
}

// Supprimer retire un cron.
func (p Planifications) Supprimer(ctx context.Context, user *User, id int64) error {
	if _, err := p.aMain(ctx, user, id); err != nil {
		return err
	}
	if err := p.Store.SupprimerPlanification(ctx, id); err != nil {
		return refus(RefusInterne, "could not delete the schedule", err)
	}
	slog.Info("cron supprimé", slog.Int64("schedule", id), slog.Int64("par", user.ID))
	return nil
}

// Planificateur déclenche les crons échus (ADR-0057).
type Planificateur struct {
	Store   PlanificationStore
	Comptes ComptesParID
	Lanceur Lanceur

	// Grace borne le retard toléré : au-delà, l'échéance est consignée comme
	// manquée et AUCUN run ne part. Le déploiement de 3 h ne doit pas partir à
	// 14 h parce que l'API vient de redémarrer.
	Grace time.Duration

	// DelaiDeLancement borne un lancement : le rafraîchissement du miroir
	// parle à la forge, qui peut ne jamais répondre.
	DelaiDeLancement time.Duration

	Horloge func() time.Time
}

// maxParTour borne un tour : au-delà, le tour suivant reprendra. Sans borne, une
// panne qui rendrait chaque réclamation identique ferait tourner la boucle à vide.
const maxParTour = 100

func (p Planificateur) maintenant() time.Time {
	if p.Horloge != nil {
		return p.Horloge()
	}
	return time.Now()
}

// Tour réclame et déclenche tous les crons échus, un à un. Rend le nombre de
// crons réclamés.
//
// RÉCLAMER → VALIDER → LANCER, dans cet ordre, donc AU PLUS UNE FOIS : un
// plantage entre la réclamation et le lancement perd ce déclenchement plutôt
// que de le doubler. Pour un playbook d'exploitation, ne pas tourner une fois
// vaut mieux que tourner deux fois.
func (p Planificateur) Tour(ctx context.Context) (int, error) {
	n := 0
	for n < maxParTour {
		if err := ctx.Err(); err != nil {
			return n, err
		}
		maintenant := p.maintenant()
		s, ok, err := p.Store.ReclamerEchue(ctx, maintenant, func(s Planification) time.Time {
			expr, err := AnalyserCron(s.Expression, s.Fuseau)
			if err != nil {
				// L'expression a été validée à la création ; si elle ne l'est
				// plus (données de fuseau changées), le cron s'arrête plutôt que
				// de tirer à chaque tour.
				return time.Time{}
			}
			// Depuis MAINTENANT et non depuis l'échéance passée : au plus UNE
			// échéance rattrapée, jamais une par occurrence manquée.
			return expr.Suivante(maintenant)
		})
		if err != nil {
			return n, fmt.Errorf("réclamation d'un cron échu : %w", err)
		}
		if !ok {
			return n, nil
		}
		n++
		p.declencher(ctx, s, maintenant)
	}
	return n, nil
}

func (p Planificateur) declencher(ctx context.Context, s Planification, maintenant time.Time) {
	journal := slog.With(slog.Int64("schedule", s.ID), slog.String("nom", s.Nom),
		slog.String("repo", s.Repo), slog.String("environnement", s.Environment))

	consigner := func(d Declenchement) {
		d.Quand = maintenant
		if err := p.Store.ConsignerDeclenchement(ctx, s.ID, d); err != nil {
			journal.Error("consignation du déclenchement en échec", slog.String("error", err.Error()))
		}
	}

	if retard := maintenant.Sub(s.ProchainDeclenche); p.Grace > 0 && retard > p.Grace {
		journal.Warn("échéance manquée, aucun run lancé",
			slog.Time("echeance", s.ProchainDeclenche), slog.Duration("retard", retard))
		consigner(Declenchement{Erreur: fmt.Sprintf(
			"missed: due at %s, %s late (grace is %s)",
			s.ProchainDeclenche.UTC().Format(time.RFC3339), retard.Truncate(time.Second), p.Grace)})
		return
	}

	if p.Comptes == nil {
		consigner(Declenchement{Erreur: "the schedule owner could not be read on this deployment"})
		return
	}
	user, err := p.Comptes.UserByID(ctx, s.ProprietaireID)
	if err != nil {
		journal.Error("lecture du propriétaire en échec", slog.String("error", err.Error()))
		consigner(Declenchement{Erreur: "the schedule owner could not be read"})
		return
	}

	lctx := ctx
	if p.DelaiDeLancement > 0 {
		var annuler context.CancelFunc
		lctx, annuler = context.WithTimeout(ctx, p.DelaiDeLancement)
		defer annuler()
	}
	id := s.ID
	demarre, err := p.Lanceur.LancerSurLaPrincipale(lctx, &user, LancementSurLaPrincipale{
		Repo:        s.Repo,
		Environment: s.Environment,
		Inventory:   s.Inventory,
		Playbook:    s.Playbook,
		Flags:       s.Flags,
		ScheduleID:  &id,
	})
	if err != nil {
		// Un échec de DÉMARRAGE rend aussi le run enregistré : on le nomme.
		journal.Warn("déclenchement refusé", slog.String("run_ref", demarre.RunRef),
			slog.String("error", err.Error()))
		consigner(Declenchement{RunRef: demarre.RunRef, Erreur: MessageDeRefus(err)})
		return
	}
	journal.Info("cron déclenché", slog.String("run_ref", demarre.RunRef),
		slog.Int64("user", user.ID))
	consigner(Declenchement{RunRef: demarre.RunRef})
}

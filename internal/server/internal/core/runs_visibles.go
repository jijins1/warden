package core

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// CriteresDeRuns est ce que l'appelant choisit dans la liste des runs : des
// critères qui RÉTRÉCISSENT, jamais ce qui borne la visibilité. Le propriétaire
// et les environnements élargis n'y figurent pas : RunsVisibles les pose depuis
// l'appelant authentifié et ses droits vivants, et rien venu de l'extérieur ne
// peut les écrire.
type CriteresDeRuns struct {
	Cursor string
	Limit  int

	Environment string
	Inventory   string
	State       string
	Since       time.Time
	Until       time.Time
	ChangedMin  int

	// SeulementLesMiens ramène la liste aux runs de l'appelant, droits ou pas.
	SeulementLesMiens bool
}

// ListeurDeRuns est la seule lecture dont RunsVisibles a besoin.
type ListeurDeRuns interface {
	ListRuns(ctx context.Context, filter RunFilter) (RunListResult, error)
}

// RunsVisibles rend la page de runs que l'utilisateur peut voir (§10.2).
//
// La règle est celle de la vue d'un run seul : on voit ses propres runs, et
// ceux d'autrui sur un environnement où l'on a un droit VIVANT.
//
//   - un environnement nommé, sur lequel l'appelant a un droit : tous ses runs,
//     sans filtre de propriétaire — une liste réduite aux siens contredirait le
//     détail qu'elle ouvre ;
//   - un environnement nommé, sans droit : les seuls runs de l'appelant ;
//   - aucun environnement : ses runs, plus tous ceux des environnements où il a
//     un droit, en une requête.
//
// Une panne de lecture des droits n'ouvre rien : la liste se rabat sur les
// runs de l'appelant. C'est l'écran d'accueil, et la vue étroite reste juste,
// seulement partielle.
func RunsVisibles(ctx context.Context, runs ListeurDeRuns, droits EnvironmentGrants, userID int64, c CriteresDeRuns) (RunListResult, error) {
	// ListRuns lit un UserID nul comme « aucun filtre » : un appelant sans
	// identité verrait tout.
	if userID <= 0 {
		return RunListResult{}, errors.New("liste des runs : appelant sans identité")
	}
	filtre := RunFilter{
		Cursor:      c.Cursor,
		Limit:       c.Limit,
		Environment: c.Environment,
		Inventory:   c.Inventory,
		State:       c.State,
		Since:       c.Since,
		Until:       c.Until,
		ChangedMin:  c.ChangedMin,
		UserID:      userID,
	}
	switch {
	case c.SeulementLesMiens:
	case c.Environment != "":
		if peutAtteindre(ctx, droits, userID, c.Environment) {
			filtre.UserID = 0
		}
	default:
		filtre.AussiEnvironnements = EnvironnementsVisibles(ctx, droits, userID)
	}
	return runs.ListRuns(ctx, filtre)
}

// EnvironnementsVisibles rend, triés, les environnements sur lesquels
// l'utilisateur a un droit vivant. Vide — jamais nil — quand il n'en a aucun ou
// qu'ils ne se lisent pas : une panne de lecture n'accorde rien.
func EnvironnementsVisibles(ctx context.Context, droits EnvironmentGrants, userID int64) []string {
	if droits == nil {
		return []string{}
	}
	envs, err := droits.EnvironnementsAtteignables(ctx, userID)
	if err != nil {
		slog.WarnContext(ctx, "lecture des environnements atteignables en échec", slog.String("error", err.Error()))
		return []string{}
	}
	if envs == nil {
		return []string{}
	}
	return envs
}

// peutAtteindre est PeutAtteindre, refus compris quand le magasin manque ou
// tombe en panne.
func peutAtteindre(ctx context.Context, droits EnvironmentGrants, userID int64, environment string) bool {
	if droits == nil {
		return false
	}
	ok, err := droits.PeutAtteindre(ctx, userID, environment)
	return err == nil && ok
}

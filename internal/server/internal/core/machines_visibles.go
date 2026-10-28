package core

import (
	"context"
	"errors"
	"slices"
)

// CriteresDeMachines est ce que l'appelant choisit dans la liste des machines :
// des critères qui RÉTRÉCISSENT. La portée n'y figure pas — MachinesVisibles la
// pose depuis les droits vivants de l'appelant, et rien venu de l'extérieur ne
// peut l'écrire.
type CriteresDeMachines struct {
	Cursor string
	Limit  int

	// Environment ramène la liste à un environnement, s'il est dans la portée.
	Environment string

	// Nom est une sous-chaîne du nom de machine, sans égard à la casse.
	Nom string
}

// FiltreDeMachines est l'interrogation remise au magasin.
//
// Pagination par keyset sur (environnement, nom), comme RunFilter : l'ordre
// est celui de la contrainte d'unicité, donc servi par son index.
type FiltreDeMachines struct {
	// Cursor est opaque pour l'appelant : il vient d'une PageDeMachines.
	Cursor string

	// Limit est la taille de page (50 par défaut, 500 au maximum).
	Limit int

	// Environnements BORNE la liste : une machine hors de ces environnements
	// n'est jamais rendue. Vide signifie « rien », PAS « tout » — à l'inverse
	// de ListHosts, et délibérément : la valeur zéro d'un filtre de portée
	// doit fermer, sinon un oubli ouvre l'inventaire entier.
	//
	// Il n'est posé que par MachinesVisibles.
	Environnements []string

	// Nom est une sous-chaîne du nom de machine, sans égard à la casse.
	Nom string
}

// PageDeMachines est une page de la liste des machines.
type PageDeMachines struct {
	Hosts []ManagedHost

	// NextCursor est vide sur la dernière page.
	NextCursor string
}

// ErrCurseurIllisible signale un curseur que le magasin ne sait pas relire.
//
// Une erreur nommée plutôt qu'un message reconnu à la chaîne : le handler en
// fait un 400, et ce choix ne doit pas dépendre du libellé d'un adaptateur.
var ErrCurseurIllisible = errors.New("invalid cursor")

// ListeurDeMachines est la seule lecture dont MachinesVisibles a besoin.
type ListeurDeMachines interface {
	// ListerMachines rend une page de machines. Un curseur illisible est une
	// erreur enveloppant ErrCurseurIllisible, pas un retour à la première page.
	ListerMachines(ctx context.Context, filtre FiltreDeMachines) (PageDeMachines, error)
}

// MachinesVisibles rend la page de machines que l'utilisateur peut voir.
//
// La portée est EXACTEMENT EnvironnementsVisibles : les environnements sur
// lesquels l'appelant a un droit vivant. Pas de cas « ses propres machines »
// comme pour les runs — une machine n'appartient à personne, elle appartient
// à un environnement. Être administrateur n'élargit rien, comme pour
// `GET /environments` : l'inventaire complet reste sous `/admin/hosts`.
//
// Un environnement nommé hors de la portée donne une liste VIDE, pas un refus :
// un refus confirmerait que l'environnement existe.
//
// Une panne de lecture des droits n'ouvre rien : la portée est vide, donc la
// liste aussi.
func MachinesVisibles(ctx context.Context, machines ListeurDeMachines, droits EnvironmentGrants, userID int64, c CriteresDeMachines) (PageDeMachines, error) {
	if userID <= 0 {
		return PageDeMachines{}, errors.New("liste des machines : appelant sans identité")
	}

	portee := EnvironnementsVisibles(ctx, droits, userID)
	if c.Environment != "" {
		if slices.Contains(portee, c.Environment) {
			portee = []string{c.Environment}
		} else {
			portee = []string{}
		}
	}

	// Le magasin est interrogé MÊME sur une portée vide : c'est lui qui relit
	// le curseur, et un curseur illisible doit rester un 400 quels que soient
	// les droits de l'appelant. Une portée vide ne lui fait rien lire d'autre.
	return machines.ListerMachines(ctx, FiltreDeMachines{
		Cursor:         c.Cursor,
		Limit:          c.Limit,
		Environnements: portee,
		Nom:            c.Nom,
	})
}

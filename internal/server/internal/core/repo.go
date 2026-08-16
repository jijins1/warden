package core

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// Repo est la politique serveur d'un dépôt : où est la forge, quelle branche est
// protégée.
//
// Elle vit côté serveur et jamais dans un fichier du dépôt. C'est la décision
// actée n°7 — « le fichier client est une requête ; le serveur détient la
// politique », leçon Atlantis du §5.4 — et elle porte ici plus qu'ailleurs :
// `MainlineRef` ancre le contrôle de fraîcheur du push (ADR-0027). Un dépôt qui
// déclarerait lui-même sa branche protégée choisirait ce que le serveur vérifie.
type Repo struct {
	Name        string
	RemoteURL   string
	MainlineRef string
}

// nomDeDepot est la MÊME forme que la contrainte `repo_name_forme` de la
// migration 00005 et que `run_repo_forme` de la 00004.
//
// Trois expressions identiques dans trois couches, et c'est voulu : ce nom
// devient un composant de chemin sous la racine des dépôts du rôle `git`. Chacune
// couvre un chemin d'entrée différent, et aucune ne peut compter sur les autres.
var nomDeDepot = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// Valid vérifie la forme d'une configuration avant qu'elle atteigne la base.
//
// Échouer ici plutôt qu'à l'INSERT rend le message utilisable : une violation de
// contrainte PostgreSQL nomme la contrainte, pas ce que l'administrateur aurait
// dû écrire.
func (r Repo) Valid() error {
	if !nomDeDepot.MatchString(r.Name) {
		return fmt.Errorf("nom de dépôt invalide : lettres, chiffres, `.`, `_` et `-`, 128 caractères au plus")
	}

	// Le schéma est revérifié par le rôle `git` avant tout usage — `ParseRemote`
	// y refuse `ext::`, qui prend une commande shell et l'exécute, et `file://`,
	// qui atteindrait les dépôts voisins sur le volume. Le contrôle est répété
	// ici pour que l'administrateur soit repoussé au moment où il se trompe,
	// plutôt qu'au premier run qui échoue.
	if !strings.HasPrefix(r.RemoteURL, "http://") && !strings.HasPrefix(r.RemoteURL, "https://") {
		return fmt.Errorf("URL de forge invalide : seuls http et https sont acceptés")
	}
	if !strings.HasPrefix(r.MainlineRef, "refs/heads/") {
		return fmt.Errorf("branche protégée invalide : nom de ref complet attendu, par exemple refs/heads/main")
	}
	if strings.Contains(r.MainlineRef, "..") || strings.ContainsAny(r.MainlineRef, " ~^:?*[\\") {
		return fmt.Errorf("branche protégée invalide : caractère refusé par git")
	}
	return nil
}

// RepoStore lit et écrit la politique par dépôt.
//
// Un port du cœur, comme les autres : l'implémentation vit dans `outbound/store`
// et le cœur ne connaît pas PostgreSQL (ADR-0019).
type RepoStore interface {
	// GetRepo rend la configuration d'un dépôt, ou ErrNotFound.
	GetRepo(ctx context.Context, name string) (Repo, error)

	// ListRepos rend toute la configuration, triée par nom.
	ListRepos(ctx context.Context) ([]Repo, error)

	// UpsertRepo écrit la configuration et retient qui l'a écrite.
	//
	// `by` est l'administrateur : la configuration d'un dépôt décide de ce qui
	// s'exécutera en production, donc savoir qui l'a changée est du même ordre
	// que savoir qui a lancé un run.
	UpsertRepo(ctx context.Context, r Repo, by int64) error
}

// RepoMaterializer crée le dépôt bare sur le volume du rôle `git`.
//
// Il existe parce que la configuration ne suffit pas : déclarer la forge et la
// branche protégée d'un dépôt ne crée rien sur le volume, et le premier run
// échoue alors sur « dépôt inconnu » — message exact, décrivant un état que rien
// ne permettait d'atteindre.
//
// Séparé de RepoStore, qui écrit la POLITIQUE. Celui-ci écrit sur un VOLUME, à
// travers le réseau, et son échec n'a pas les mêmes conséquences : une
// configuration sans dépôt se répare en rejouant, un dépôt sans configuration est
// un répertoire inerte.
type RepoMaterializer interface {
	// CreateRepo est idempotente : un dépôt déjà présent est un succès.
	CreateRepo(ctx context.Context, repo string) error
}

// MirrorRefresher rafraîchit le miroir de la branche protégée et rend sa
// position.
//
// Séparé de RepoDiffer bien que la même arête le serve : les deux répondent à
// des questions sans rapport, et un port qui mélange « lis-moi un diff » et
// « va chercher du code sur Internet » cesse de dire quelque chose sur ses
// implémentations.
type MirrorRefresher interface {
	// RefreshMirror rend le SHA de la branche protégée après rafraîchissement.
	//
	// C'est cette valeur que le run FIGE : sans gel, le diff « run contre
	// principale » du §6.5 se recalculerait contre la branche d'aujourd'hui et
	// changerait à chaque consultation (ADR-0021).
	RefreshMirror(ctx context.Context, repo, remote, ref string) (string, error)
}

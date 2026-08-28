package core

import (
	"context"
	"errors"
	"fmt"

	"github.com/jijins1/warden/internal/config"
)

// ErrNoProjectConfig signale que le commit ne porte pas de fichier de
// configuration.
//
// C'est un état ORDINAIRE et non une panne : un dépôt qui n'a pas encore adopté
// le fichier doit continuer de tourner. L'ADR-0009 prévoit qu'à terme un run
// sans configuration soit refusé ; ce refus s'activera quand les dépôts en
// auront un, et il s'appuiera sur cette sentinelle plutôt que sur un test de
// chaîne vide — sans quoi « fichier absent » et « fichier vide » deviendraient
// indiscernables, alors que le second est une déclaration délibérée.
var ErrNoProjectConfig = errors.New("ce commit ne porte pas de fichier de configuration")

// ProjectConfigReader lit un fichier dans l'arbre d'un commit.
//
// Déclaré ICI, du côté qui l'appelle, comme les autres ports (ADR-0019).
// L'adaptateur `outbound/gitclient` le satisfait structurellement, sans avoir à
// le nommer.
//
// Il rend des OCTETS et non une configuration analysée : le transport ne
// comprend pas ce qu'il transporte. L'analyse appartient à `internal/config`, la
// composition à ce paquet — c'est la répartition que l'ADR-0009 pose et que
// `config/doc.go` répète.
type ProjectConfigReader interface {
	// ReadProjectConfig rend le contenu brut du fichier de configuration du
	// commit, ou une erreur enveloppant ErrNoProjectConfig quand il n'y en a pas.
	ReadProjectConfig(ctx context.Context, repo, sha string) ([]byte, error)
}

// ExigenceConfig dit si un commit sans fichier de configuration est refusé.
//
// L'ADR-0009 tranche que le refus est le comportement voulu : « fichier absent
// ou illisible dans le commit → run refusé », et l'ADR-0003 pose déjà qu'un
// dépôt sans commit ne peut pas porter de configuration valide.
//
// Il est pourtant FACULTATIF ici, et c'est une décision de MIGRATION, pas de
// conception. Activer le refus d'un coup arrêterait tous les runs de tous les
// dépôts qui n'ont pas encore adopté le fichier — y compris ceux qu'un
// exploitant ne contrôle pas directement. L'interrupteur existe pour que la
// bascule soit un geste délibéré, fait quand les dépôts sont prêts.
//
// Il est destiné à DISPARAÎTRE : le jour où `ConfigObligatoire` est le seul
// comportement déployé, ce type et sa variable d'environnement partent avec lui.
// Un interrupteur de migration qui survit à sa migration devient une option, et
// une option se met à devoir être supportée.
type ExigenceConfig bool

const (
	// ConfigFacultative : l'absence rend une configuration nulle et le run part.
	ConfigFacultative ExigenceConfig = false

	// ConfigObligatoire : l'absence refuse le run (ADR-0009).
	ConfigObligatoire ExigenceConfig = true
)

// ProjectConfigFor résout la configuration de projet d'un commit.
//
// C'est le point où l'ADR-0009 devient exécutable : ce qui S'APPLIQUE est lu
// dans le commit, donc c'est le même octet que ce qui est AUDITÉ. Aucun champ de
// configuration ne vient de la requête, et une CLI modifiée ne peut pas faire
// diverger l'exécution de la trace.
//
// Un lecteur nil rend une configuration vide sans erreur, QUELLE QUE SOIT
// l'exigence : un déploiement qui n'a pas branché le rôle `git` sur ce chemin
// n'a pas les moyens de savoir si le fichier existe, et refuser tous ses runs
// sur une question qu'on ne peut pas poser serait un refus mensonger.
//
// Le contenu est une ENTRÉE NON FIABLE et `config.Parse` le traite comme telle :
// taille plafonnée, champs inconnus refusés, ancres et alias refusés.
func ProjectConfigFor(ctx context.Context, lecteur ProjectConfigReader, repo, sha string,
	exigence ExigenceConfig,
) (config.Project, error) {
	if lecteur == nil {
		return config.Project{}, nil
	}
	if repo == "" || sha == "" {
		// Ni l'un ni l'autre n'est devinable, et demander au rôle `git` un blob
		// dans un dépôt anonyme produirait une erreur de transport très loin de
		// la cause.
		return config.Project{}, fmt.Errorf("configuration de projet : dépôt et commit sont requis")
	}

	brut, err := lecteur.ReadProjectConfig(ctx, repo, sha)
	if err != nil {
		if errors.Is(err, ErrNoProjectConfig) {
			if exigence == ConfigObligatoire {
				// Le message nomme le fichier ET dit quoi faire : l'utilisateur
				// n'a aucun moyen de deviner ce nom, et un « configuration
				// requise » nu l'enverrait ouvrir un ticket.
				return config.Project{}, fmt.Errorf(
					"ce commit ne porte pas de %s : ce dépôt doit en committer un à sa racine", config.File)
			}
			return config.Project{}, nil
		}
		return config.Project{}, fmt.Errorf("lecture de %s dans %s : %w", config.File, sha[:min(12, len(sha))], err)
	}

	projet, err := config.Parse(brut)
	if err != nil {
		// Le message de `config.Parse` NOMME déjà la clé ou la construction en
		// cause (ADR-0009) : l'envelopper suffit, le reformuler le noierait.
		return config.Project{}, fmt.Errorf("configuration du projet : %w", err)
	}
	return projet, nil
}

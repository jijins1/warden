// Package protectedfile lit la liste des environnements protégés depuis un
// fichier monté.
//
// C'est la source de vérité (ADR-0044) : un ConfigMap versionné dans le dépôt
// de déploiement, monté dans le pod contrôleur — même mécanisme que le
// fichier de droits (ADR-0033), en fichier séparé parce que les deux notions
// sont distinctes : un droit contrôle un ACCÈS, ceci ne fait qu'avertir.
//
// Ce paquet ne fait que LIRE et détecter le changement, comme grantfile. La
// validation et le calcul de ce qui change sont dans le cœur.
package protectedfile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"sigs.k8s.io/yaml"

	"github.com/jijins1/warden/internal/server/internal/core"
)

// Source lit le document depuis un chemin, et retient l'EMPREINTE du dernier
// contenu lu pour que le réconciliateur ne fasse rien quand rien n'a bougé.
type Source struct {
	chemin  string
	dernier string
}

// New construit la source. Un chemin vide rend nil, ce que l'appelant traite
// comme « pas de source configurée ».
func New(chemin string) *Source {
	if chemin == "" {
		return nil
	}
	return &Source{chemin: chemin}
}

// Chemin rend le fichier lu, pour la journalisation.
func (s *Source) Chemin() string { return s.chemin }

// ErrAbsent signale que le fichier n'existe pas.
//
// Distinct d'une erreur de lecture : un contrôleur démarré avant que le
// ConfigMap soit posé ne doit RIEN faire, plutôt que traiter l'absence comme
// une liste vide et retirer la protection de tout le monde.
var ErrAbsent = errors.New("aucun fichier d'environnements protégés")

// Lire rend le document, et dit s'il a CHANGÉ depuis la dernière lecture.
//
// Même logique que grantfile.Source.Lire : le changement est détecté sur le
// CONTENU et non sur la date, et un contenu illisible n'efface pas
// l'empreinte retenue.
func (s *Source) Lire() (doc core.ProtectedDocument, change bool, err error) {
	brut, err := os.ReadFile(s.chemin) // #nosec G304 -- chemin de configuration du déploiement
	if errors.Is(err, fs.ErrNotExist) {
		return core.ProtectedDocument{}, false, ErrAbsent
	}
	if err != nil {
		return core.ProtectedDocument{}, false, fmt.Errorf("lecture de %s : %w", s.chemin, err)
	}

	empreinte := string(brut)
	if empreinte == s.dernier {
		return core.ProtectedDocument{}, false, nil
	}

	if err := yaml.Unmarshal(brut, &doc); err != nil {
		return core.ProtectedDocument{}, false, fmt.Errorf("%s est illisible : %w", s.chemin, err)
	}

	s.dernier = empreinte
	return doc, true, nil
}

// Oublier force la relecture au prochain passage.
func (s *Source) Oublier() { s.dernier = "" }

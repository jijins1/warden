// Package grantfile lit les droits par environnement depuis un fichier monté.
//
// C'est la source de vérité des droits (ADR-0033) : un ConfigMap versionné dans
// le dépôt de déploiement, synchronisé par l'outil GitOps, monté dans le pod. Le
// produit ne fournit AUCUNE commande pour modifier les droits — la seule façon
// d'en changer est de committer.
//
// Ce paquet ne fait que LIRE et détecter le changement. La différence entre ce
// qui est voulu et ce qui est en base est calculée par le cœur, qui n'a pas à
// savoir d'où vient le document.
package grantfile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"sigs.k8s.io/yaml"

	"github.com/jijins1/warden/internal/server/internal/core"
)

// Source lit le document de droits depuis un chemin.
//
// Elle retient l'EMPREINTE du dernier contenu lu, pour que le réconciliateur
// puisse ne rien faire quand rien n'a bougé — ce qui est le cas à tous les
// passages sauf un.
type Source struct {
	chemin  string
	dernier string
}

// New construit la source. Un chemin vide rend nil, ce que l'appelant traite
// comme « pas de source de droits configurée ».
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
// Distinct d'une erreur de lecture, et l'appelant DOIT le traiter comme « ne
// rien faire » plutôt que comme un document vide. Un contrôleur démarré avant
// que le ConfigMap soit posé révoquerait sinon tous les droits du produit.
var ErrAbsent = errors.New("aucun fichier de droits")

// Lire rend le document, et dit s'il a CHANGÉ depuis la dernière lecture.
//
// Le changement est détecté sur le CONTENU et non sur la date : un ConfigMap
// monté est republié par le kubelet à chaque resynchronisation, y compris quand
// rien n'a bougé, et se fier à l'horodatage ferait rejouer une réconciliation
// toutes les minutes pour rien.
//
// Un contenu illisible n'efface PAS l'empreinte retenue : sans cela, un YAML
// cassé ferait signaler un changement à chaque passage, donc une erreur toutes
// les trente secondes dans les journaux.
func (s *Source) Lire() (doc core.GrantDocument, change bool, err error) {
	brut, err := os.ReadFile(s.chemin) // #nosec G304 -- chemin de configuration du déploiement
	if errors.Is(err, fs.ErrNotExist) {
		return core.GrantDocument{}, false, ErrAbsent
	}
	if err != nil {
		return core.GrantDocument{}, false, fmt.Errorf("lecture de %s : %w", s.chemin, err)
	}

	empreinte := string(brut)
	if empreinte == s.dernier {
		return core.GrantDocument{}, false, nil
	}

	if err := yaml.Unmarshal(brut, &doc); err != nil {
		return core.GrantDocument{}, false, fmt.Errorf("%s est illisible : %w", s.chemin, err)
	}

	// L'empreinte n'est retenue qu'une fois le document LU avec succès. Une
	// version cassée ne doit pas être considérée comme « déjà traitée » : elle
	// sera relue, et le jour où elle est corrigée le changement se voit.
	s.dernier = empreinte
	return doc, true, nil
}

// Oublier force la relecture au prochain passage.
//
// Appelée quand l'application du plan a échoué : le document n'a pas pris effet,
// donc il ne doit pas être considéré comme traité.
func (s *Source) Oublier() { s.dernier = "" }

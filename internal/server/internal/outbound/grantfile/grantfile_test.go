// Étage 1 de l'ADR-0010 : ni base, ni cluster.
//
// Ce lecteur décide si un document de droits doit être réappliqué. Les deux
// erreurs qui coûteraient cher sont symétriques : conclure « rien à faire » sur
// un fichier qui a changé, et conclure « personne n'a de droit » sur un fichier
// qu'on n'a pas su lire.

package grantfile_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jijins1/warden/internal/server/internal/outbound/grantfile"
)

const document = `environments:
  prod:
    - alice
    - bob
`

// poser écrit un document et rend son chemin.
func poser(t *testing.T, contenu string) string {
	t.Helper()
	chemin := filepath.Join(t.TempDir(), "grants.yaml")
	if err := os.WriteFile(chemin, []byte(contenu), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return chemin
}

func TestUnDocumentSeLit(t *testing.T) {
	t.Parallel()

	s := grantfile.New(poser(t, document))

	doc, change, err := s.Lire()
	if err != nil {
		t.Fatalf("Lire: %v", err)
	}
	if !change {
		t.Error("la première lecture ne signale pas de changement")
	}
	if len(doc.Environments["prod"]) != 2 {
		t.Errorf("document = %+v", doc)
	}
}

// LE test qui évite une réconciliation par minute. Un ConfigMap monté est
// republié par le kubelet à chaque resynchronisation, y compris quand rien n'a
// bougé : se fier à l'horodatage ferait rejouer le plan indéfiniment.
func TestUneRelectureIdentiqueNeSignalePasDeChangement(t *testing.T) {
	t.Parallel()

	chemin := poser(t, document)
	s := grantfile.New(chemin)

	if _, change, _ := s.Lire(); !change {
		t.Fatal("première lecture sans changement")
	}
	for i := range 5 {
		_, change, err := s.Lire()
		if err != nil {
			t.Fatalf("lecture %d : %v", i, err)
		}
		if change {
			t.Errorf("lecture %d : changement signalé alors que le contenu est identique", i)
		}
	}

	// Le même contenu RÉÉCRIT — ce que fait le kubelet — ne change rien non plus.
	if err := os.WriteFile(chemin, []byte(document), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, change, _ := s.Lire(); change {
		t.Error("une réécriture du même contenu a été prise pour un changement")
	}
}

func TestUnContenuModifieEstSignale(t *testing.T) {
	t.Parallel()

	chemin := poser(t, document)
	s := grantfile.New(chemin)
	_, _, _ = s.Lire()

	if err := os.WriteFile(chemin, []byte("environments:\n  prod:\n    - alice\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	doc, change, err := s.Lire()
	if err != nil {
		t.Fatalf("Lire: %v", err)
	}
	if !change {
		t.Fatal("le changement n'a pas été détecté")
	}
	if len(doc.Environments["prod"]) != 1 {
		t.Errorf("document = %+v", doc)
	}
}

// L'AUTRE erreur coûteuse. Un contrôleur démarré avant que le ConfigMap soit
// posé ne doit pas conclure « personne n'a de droit » et tout révoquer.
func TestUnFichierAbsentEstDistinctDUnDocumentVide(t *testing.T) {
	t.Parallel()

	s := grantfile.New(filepath.Join(t.TempDir(), "jamais-ecrit.yaml"))

	doc, change, err := s.Lire()
	if !errors.Is(err, grantfile.ErrAbsent) {
		t.Fatalf("err = %v, attendu ErrAbsent", err)
	}
	if change {
		t.Error("un fichier absent a été signalé comme un changement")
	}
	if len(doc.Environments) != 0 {
		t.Errorf("document = %+v", doc)
	}
}

// Un YAML cassé ne doit PAS être retenu comme traité : sans quoi le corriger
// plus tard ne serait pas vu comme un changement, et le document resterait sans
// effet jusqu'au redémarrage du contrôleur.
func TestUnYamlCasseResteRelu(t *testing.T) {
	t.Parallel()

	chemin := poser(t, "environments: [ceci n'est pas")
	s := grantfile.New(chemin)

	if _, _, err := s.Lire(); err == nil {
		t.Fatal("un YAML cassé a été accepté")
	}

	// Corrigé : le changement doit se voir.
	if err := os.WriteFile(chemin, []byte(document), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, change, err := s.Lire()
	if err != nil {
		t.Fatalf("Lire: %v", err)
	}
	if !change {
		t.Error("la correction d'un document cassé n'a pas été vue")
	}
}

// Oublier force la relecture. Elle est appelée quand l'application du plan a
// échoué : le document n'a pas pris effet, donc il ne doit pas être considéré
// comme traité.
func TestOublierForceLaRelecture(t *testing.T) {
	t.Parallel()

	s := grantfile.New(poser(t, document))
	_, _, _ = s.Lire()

	if _, change, _ := s.Lire(); change {
		t.Fatal("changement signalé sans modification")
	}

	s.Oublier()

	if _, change, _ := s.Lire(); !change {
		t.Error("après Oublier, le document n'est pas relu")
	}
}

// Un chemin vide rend nil : « pas de source de droits configurée ». C'est ce que
// l'appelant traite comme « ne rien réconcilier ».
func TestUnCheminVideRendNil(t *testing.T) {
	t.Parallel()

	if s := grantfile.New(""); s != nil {
		t.Errorf("New(\"\") = %v, attendu nil", s)
	}
}

// Un document syntaxiquement valide mais VIDE se lit sans erreur ici : c'est le
// planificateur qui le refuse, parce que c'est lui qui sait ce que « vide »
// impliquerait. Ce lecteur ne fait que lire.
func TestUnDocumentVideSeLitSansErreur(t *testing.T) {
	t.Parallel()

	s := grantfile.New(poser(t, "environments: {}\n"))

	doc, change, err := s.Lire()
	if err != nil {
		t.Fatalf("Lire: %v", err)
	}
	if !change || len(doc.Environments) != 0 {
		t.Errorf("change=%v document=%+v", change, doc)
	}
}

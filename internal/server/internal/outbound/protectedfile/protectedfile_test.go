// Étage 1 de l'ADR-0010 : ni base, ni cluster.
//
// Même charpente que grantfile_test.go : les deux erreurs qui coûtent cher
// sont symétriques — conclure « rien à faire » sur un fichier modifié, et
// conclure « rien n'est déclaré » sur un fichier qu'on n'a pas su lire.

package protectedfile_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jijins1/warden/internal/server/internal/outbound/protectedfile"
)

const document = `environments:
  prod:
    protected: true
    allowRun: false
  preprod:
    protected: true
`

func poser(t *testing.T, contenu string) string {
	t.Helper()
	chemin := filepath.Join(t.TempDir(), "protected.yaml")
	if err := os.WriteFile(chemin, []byte(contenu), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return chemin
}

func TestUnDocumentSeLit(t *testing.T) {
	t.Parallel()

	s := protectedfile.New(poser(t, document))

	doc, change, err := s.Lire()
	if err != nil {
		t.Fatalf("Lire: %v", err)
	}
	if !change {
		t.Error("la première lecture ne signale pas de changement")
	}
	if len(doc.Environments) != 2 {
		t.Errorf("document = %+v", doc)
	}
	if doc.Environments["prod"].AllowRun == nil || *doc.Environments["prod"].AllowRun {
		t.Errorf("prod.allowRun = %+v, attendu false", doc.Environments["prod"])
	}
}

func TestUneRelectureIdentiqueNeSignalePasDeChangement(t *testing.T) {
	t.Parallel()

	chemin := poser(t, document)
	s := protectedfile.New(chemin)

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
}

func TestUnContenuModifieEstSignale(t *testing.T) {
	t.Parallel()

	chemin := poser(t, document)
	s := protectedfile.New(chemin)
	_, _, _ = s.Lire()

	if err := os.WriteFile(chemin, []byte("environments:\n  prod:\n    protected: true\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	doc, change, err := s.Lire()
	if err != nil {
		t.Fatalf("Lire: %v", err)
	}
	if !change {
		t.Fatal("le changement n'a pas été détecté")
	}
	if len(doc.Environments) != 1 {
		t.Errorf("document = %+v", doc)
	}
}

// L'AUTRE erreur coûteuse. Un contrôleur démarré avant que le ConfigMap soit
// posé ne doit pas conclure « rien n'est déclaré » et retirer tous les gels et
// tous les badges.
func TestUnFichierAbsentEstDistinctDUnDocumentVide(t *testing.T) {
	t.Parallel()

	s := protectedfile.New(filepath.Join(t.TempDir(), "jamais-ecrit.yaml"))

	doc, change, err := s.Lire()
	if !errors.Is(err, protectedfile.ErrAbsent) {
		t.Fatalf("err = %v, attendu ErrAbsent", err)
	}
	if change {
		t.Error("un fichier absent a été signalé comme un changement")
	}
	if len(doc.Environments) != 0 {
		t.Errorf("document = %+v", doc)
	}
}

func TestUnYamlCasseResteRelu(t *testing.T) {
	t.Parallel()

	chemin := poser(t, "environments: [ceci n'est pas")
	s := protectedfile.New(chemin)

	if _, _, err := s.Lire(); err == nil {
		t.Fatal("un YAML cassé a été accepté")
	}

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

func TestOublierForceLaRelecture(t *testing.T) {
	t.Parallel()

	s := protectedfile.New(poser(t, document))
	_, _, _ = s.Lire()

	if _, change, _ := s.Lire(); change {
		t.Fatal("changement signalé sans modification")
	}

	s.Oublier()

	if _, change, _ := s.Lire(); !change {
		t.Error("après Oublier, le document n'est pas relu")
	}
}

func TestUnCheminVideRendNil(t *testing.T) {
	t.Parallel()

	if s := protectedfile.New(""); s != nil {
		t.Errorf("New(\"\") = %v, attendu nil", s)
	}
}

// Contrairement au document des droits, une carte VIDE ici n'est refusée par
// personne — c'est le planificateur (core.ValiderLesProteges) qui décide que
// « rien n'est déclaré » est un état valide. Ce lecteur ne fait que lire.
func TestUnDocumentVideSeLitSansErreur(t *testing.T) {
	t.Parallel()

	s := protectedfile.New(poser(t, "environments: {}\n"))

	doc, change, err := s.Lire()
	if err != nil {
		t.Fatalf("Lire: %v", err)
	}
	if !change || len(doc.Environments) != 0 {
		t.Errorf("change=%v document=%+v", change, doc)
	}
}

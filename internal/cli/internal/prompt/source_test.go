package prompt_test

import (
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jijins1/warden/internal/cli/internal/prompt"
)

func fichierAvec(t *testing.T, contenu string) string {
	t.Helper()
	chemin := filepath.Join(t.TempDir(), "valeur")
	if err := os.WriteFile(chemin, []byte(contenu), 0o600); err != nil {
		t.Fatalf("écriture : %v", err)
	}
	return chemin
}

// Les trois drapeaux sont nommés de la même façon partout. L'uniformité EST la
// réutilisation : qui a appris `--key-file` sait écrire `--password-file`.
func TestDeclarerPoseTroisDrapeauxUniformes(t *testing.T) {
	t.Parallel()

	fs := flag.NewFlagSet("essai", flag.ContinueOnError)
	fs.SetOutput(nil)
	s := prompt.Declarer(fs, "password", "le mot de passe du compte")

	for _, nom := range []string{"ask-password", "password-file", "password-stdin"} {
		if fs.Lookup(nom) == nil {
			t.Errorf("drapeau --%s absent", nom)
		}
	}

	// Et AUCUN drapeau ne prend la valeur elle-même : la ligne de commande d'un
	// processus est lisible par tout utilisateur de la machine.
	fs.VisitAll(func(f *flag.Flag) {
		if f.Name == "password" {
			t.Error("un drapeau --password prend la valeur en clair sur argv")
		}
	})

	if s.Fournie() {
		t.Error("une source non analysée se déclare fournie")
	}
}

func TestUneSourceLitLeFichier(t *testing.T) {
	t.Parallel()

	s := &prompt.Source{Quoi: "la clé privée", Fichier: fichierAvec(t, "hunter2\n")}
	v, err := s.Lire(t.Context())
	if err != nil {
		t.Fatalf("Lire: %v", err)
	}
	if string(v.Reveal()) != "hunter2" {
		t.Errorf("valeur = %q", v.Reveal())
	}
	if !s.Fournie() {
		t.Error("une source avec fichier ne se déclare pas fournie")
	}
}

// Un fichier vide nomme LE FICHIER, pas « saisie vide » : c'est ce que
// l'utilisateur doit aller regarder.
func TestUnFichierVideNommeLeFichier(t *testing.T) {
	t.Parallel()

	chemin := fichierAvec(t, "")
	s := &prompt.Source{Quoi: "la clé privée", Fichier: chemin}
	_, err := s.Lire(t.Context())
	if err == nil {
		t.Fatal("un fichier vide a été accepté")
	}
	if !strings.Contains(err.Error(), chemin) {
		t.Errorf("erreur = %v, elle devrait nommer le fichier", err)
	}
}

func TestUnFichierAbsentEstRefuse(t *testing.T) {
	t.Parallel()

	s := &prompt.Source{Quoi: "le mot de passe", Fichier: "/n/existe/pas"}
	if _, err := s.Lire(t.Context()); err == nil {
		t.Fatal("un fichier absent a été accepté")
	}
}

// Choisir silencieusement entre deux provenances serait le pire des deux
// mondes : la commande réussirait, et personne ne saurait laquelle a servi.
func TestDeuxProvenancesSExcluent(t *testing.T) {
	t.Parallel()

	cas := map[string]prompt.Source{
		"fichier et stdin":  {Quoi: "x", Fichier: "/tmp/x", Stdin: true},
		"fichier et invite": {Quoi: "x", Fichier: "/tmp/x", Demande: true},
		"stdin et invite":   {Quoi: "x", Stdin: true, Demande: true},
	}
	for nom, s := range cas {
		t.Run(nom, func(t *testing.T) {
			t.Parallel()
			if _, err := s.Lire(t.Context()); err == nil {
				t.Fatal("accepté à tort")
			}
		})
	}
}

// Sans terminal ET sans provenance, la commande ÉCHOUE — elle ne lit pas
// l'entrée standard « au cas où ». C'est la règle qui empêche un
// `warden … < fichier.yml` de prendre la première ligne pour un mot de passe.
func TestSansTerminalEtSansProvenanceLaLectureEchoue(t *testing.T) {
	t.Parallel()

	// Sous `go test`, l'entrée standard n'est pas un terminal.
	s := &prompt.Source{Quoi: "le mot de passe du compte"}
	_, err := s.Lire(t.Context())
	if err == nil {
		t.Fatal("la lecture a réussi sans provenance ni terminal")
	}
	// Le message nomme la chose attendue, pas un drapeau : c'est ce que
	// l'utilisateur cherche.
	if !strings.Contains(err.Error(), "mot de passe") {
		t.Errorf("erreur = %v", err)
	}
}

// Une valeur multiligne ne se saisit PAS au terminal : l'utilisateur ne verrait
// rien de ce qu'il colle et ne saurait pas où il en est.
func TestUneValeurMultiligneRefuseLInvite(t *testing.T) {
	t.Parallel()

	s := &prompt.Source{Quoi: "la clé privée", Multiligne: true, Demande: true}
	_, err := s.Lire(t.Context())
	if err == nil {
		t.Fatal("une invite a été acceptée pour une valeur multiligne")
	}
	if !strings.Contains(err.Error(), "fichier") {
		t.Errorf("erreur = %v, elle devrait dire quoi faire à la place", err)
	}

	// Et sans provenance non plus : le repli sur l'invite ne s'applique pas.
	sansRien := &prompt.Source{Quoi: "la clé privée", Multiligne: true}
	if _, err := sansRien.Lire(t.Context()); err == nil {
		t.Fatal("une valeur multiligne a été lue sans provenance")
	}
}

// Le multiligne traverse intact : une clé amputée d'une ligne est une clé
// illisible.
func TestUneValeurMultiligneTraverseIntacte(t *testing.T) {
	t.Parallel()

	const pem = "-----BEGIN OPENSSH PRIVATE KEY-----\nAAAA\nBBBB\n-----END OPENSSH PRIVATE KEY-----\n"

	s := &prompt.Source{Quoi: "la clé privée", Multiligne: true, Fichier: fichierAvec(t, pem)}
	v, err := s.Lire(t.Context())
	if err != nil {
		t.Fatalf("Lire: %v", err)
	}
	if string(v.Reveal()) != strings.TrimRight(pem, "\n") {
		t.Errorf("valeur = %q", v.Reveal())
	}
}

// `Fournie` sert aux valeurs FACULTATIVES : le mot de passe de `become` ne se
// demande que si on l'a réclamé.
func TestFournieDistingueLeFacultatif(t *testing.T) {
	t.Parallel()

	if (&prompt.Source{Quoi: "x"}).Fournie() {
		t.Error("une source vide se déclare fournie")
	}
	for nom, s := range map[string]prompt.Source{
		"invite":  {Quoi: "x", Demande: true},
		"fichier": {Quoi: "x", Fichier: "/tmp/x"},
		"stdin":   {Quoi: "x", Stdin: true},
	} {
		if !s.Fournie() {
			t.Errorf("%s : ne se déclare pas fournie", nom)
		}
	}
}

// L'abréviation existe pour que le réflexe `ansible-playbook` ne se heurte pas à
// un « flag provided but not defined ». Le §5.1 fait de cette familiarité
// l'objectif d'expérience du produit.
func TestUneAbreviationVautLaFormeLongue(t *testing.T) {
	t.Parallel()

	cas := []struct {
		nom  string
		args []string
	}{
		{"forme longue", []string{"--ask-become-pass"}},
		{"abréviation", []string{"-K"}},
		// Les DEUX ensemble ne sont pas une erreur : elles écrivent le même
		// booléen. Quelqu'un qui les combine par habitude ne doit pas être puni.
		{"les deux", []string{"-K", "--ask-become-pass"}},
	}

	for _, c := range cas {
		t.Run(c.nom, func(t *testing.T) {
			t.Parallel()

			fs := flag.NewFlagSet("submit", flag.ContinueOnError)
			fs.SetOutput(io.Discard)
			s := prompt.Declarer(fs, "become-pass", "le mot de passe d'élévation")
			s.Abreger(fs, "K", "abréviation d'--ask-become-pass")

			if err := fs.Parse(c.args); err != nil {
				t.Fatalf("Parse(%v) : %v", c.args, err)
			}
			if !s.Fournie() {
				t.Errorf("%v ne désigne aucune provenance", c.args)
			}
		})
	}
}

// Sans aucun des deux, rien n'est demandé : le mot de passe d'élévation est
// FACULTATIF, et le réclamer d'office ferait bloquer chaque run sur une invite.
func TestSansAbreviationNiFormeLongueRienNestDemande(t *testing.T) {
	t.Parallel()

	fs := flag.NewFlagSet("submit", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	s := prompt.Declarer(fs, "become-pass", "le mot de passe d'élévation")
	s.Abreger(fs, "K", "abréviation d'--ask-become-pass")

	if err := fs.Parse([]string{"--repo=x"}); err == nil {
		t.Fatal("un drapeau inconnu devrait être refusé")
	}
	if s.Fournie() {
		t.Error("une provenance a été désignée sans qu'on la demande")
	}
}

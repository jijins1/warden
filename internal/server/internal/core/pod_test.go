// Étage 1 de l'ADR-0010 : ni base, ni conteneur, ni cluster.
//
// Ce que ce fichier tient est une propriété de sécurité, pas une propriété de
// type : l'ADR-0018 interdit de journaliser ce qui transite par le canal de
// dispatch, et l'ADR-0023 fait du jeton d'amorçage la première valeur soumise à
// cette interdiction. Un masquage non testé est une intention.
package core_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/jijins1/warden/internal/server/internal/core"
)

func TestJetonDAmorcageEstAleatoireEt256Bits(t *testing.T) {
	vus := make(map[string]bool, 64)
	for i := 0; i < 64; i++ {
		tok, err := core.NewPodToken()
		if err != nil {
			t.Fatalf("NewPodToken: %v", err)
		}
		brut, err := base64.RawURLEncoding.DecodeString(tok.Reveal())
		if err != nil {
			t.Fatalf("le jeton n'est pas du base64url: %v", err)
		}
		if len(brut) != 32 {
			t.Fatalf("jeton de %d octets, attendu 32", len(brut))
		}
		if vus[tok.Reveal()] {
			t.Fatal("deux jetons identiques tirés")
		}
		vus[tok.Reveal()] = true
	}
}

func TestEmpreinteStableEtDistincte(t *testing.T) {
	a, err := core.NewPodToken()
	if err != nil {
		t.Fatalf("NewPodToken: %v", err)
	}
	b, err := core.NewPodToken()
	if err != nil {
		t.Fatalf("NewPodToken: %v", err)
	}

	if len(a.Hash()) != 32 {
		t.Fatalf("empreinte de %d octets, attendu 32", len(a.Hash()))
	}
	// Stabilité : deux appels rendent la même empreinte. Sans elle, une
	// authentification réussirait ou non selon l'appel.
	premiere, seconde := string(a.Hash()), string(a.Hash())
	if premiere != seconde {
		t.Error("l'empreinte n'est pas stable")
	}
	if string(a.Hash()) == string(b.Hash()) {
		t.Error("deux jetons distincts ont la même empreinte")
	}
	// L'authentification étant une recherche PAR empreinte, celle-ci ne doit
	// évidemment pas être le jeton lui-même.
	if string(a.Hash()) == a.Reveal() {
		t.Error("l'empreinte est le jeton")
	}
}

// Le cœur de la protection : les trois chemins par lesquels une valeur finit
// dans un log ou une réponse sans que personne l'ait décidé.
func TestJetonMasqueDansLesImpressions(t *testing.T) {
	tok, err := core.NewPodToken()
	if err != nil {
		t.Fatalf("NewPodToken: %v", err)
	}
	clair := tok.Reveal()

	enveloppe, err := json.Marshal(struct {
		Pod   string         `json:"pod"`
		Token core.PodToken  `json:"token"`
		Ptr   *core.PodToken `json:"ptr"`
	}{Pod: "warden-runner-aaaa", Token: tok, Ptr: &tok})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	// L'indirection est volontaire : appeler String() directement testerait la
	// méthode, alors que ce qui compte est le chemin par lequel une valeur finit
	// dans un log — un verbe de formatage appliqué à une valeur quelconque.
	imprime := func(format string, v any) string { return fmt.Sprintf(format, v) }

	rendus := map[string]string{
		"%v":            imprime("%v", tok),
		"%s":            imprime("%s", tok),
		"%+v":           imprime("%+v", tok),
		"%#v":           imprime("%#v", tok),
		"%v (struct)":   imprime("%v", struct{ T core.PodToken }{tok}),
		"%v (pointeur)": imprime("%v", &tok),
		"json":          string(enveloppe),
	}
	for forme, rendu := range rendus {
		if strings.Contains(rendu, clair) {
			t.Errorf("le jeton apparaît sous %s: %s", forme, rendu)
		}
	}
}

func TestParsePodToken(t *testing.T) {
	tok, err := core.NewPodToken()
	if err != nil {
		t.Fatalf("NewPodToken: %v", err)
	}

	relu, err := core.ParsePodToken(tok.Reveal())
	if err != nil {
		t.Fatalf("ParsePodToken: %v", err)
	}
	if string(relu.Hash()) != string(tok.Hash()) {
		t.Error("le jeton relu n'a pas la même empreinte")
	}

	// La validation de forme n'est pas un contrôle de sécurité — celui-ci est
	// la recherche par empreinte. Elle évite d'interroger la base pour rien.
	refuses := map[string]string{
		"vide":             "",
		"pas du base64":    "ceci n'est pas du base64!!",
		"trop court":       base64.RawURLEncoding.EncodeToString(make([]byte, 16)),
		"trop long":        base64.RawURLEncoding.EncodeToString(make([]byte, 64)),
		"base64 standard":  base64.StdEncoding.EncodeToString(make([]byte, 32)) + "=",
		"jeton avec marge": " " + tok.Reveal(),
	}
	for nom, s := range refuses {
		if _, err := core.ParsePodToken(s); err == nil {
			t.Errorf("un jeton %s a été accepté", nom)
		}
	}
}

func TestJetonNul(t *testing.T) {
	var nul core.PodToken
	if !nul.Zero() {
		t.Error("le jeton nul ne se déclare pas nul")
	}
	tok, err := core.NewPodToken()
	if err != nil {
		t.Fatalf("NewPodToken: %v", err)
	}
	if tok.Zero() {
		t.Error("un jeton tiré se déclare nul")
	}
}

// Un pod chaud passe l'essentiel de son existence authentifié et sans run.
func TestPodIdentityNonReclamee(t *testing.T) {
	if (core.PodIdentity{PodID: 7}).Claimed() {
		t.Error("un pod sans run se déclare réclamé")
	}
	if !(core.PodIdentity{PodID: 7, RunID: 3}).Claimed() {
		t.Error("un pod avec run ne se déclare pas réclamé")
	}
}

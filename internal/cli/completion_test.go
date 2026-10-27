package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jijins1/warden/internal/cli/internal/command"
)

func TestCommandeCompletionEcritLeScriptDuShellDemande(t *testing.T) {
	cas := map[string]string{
		"bash": "_warden_complete",
		"zsh":  "compdef",
	}
	for shell, marqueur := range cas {
		t.Run(shell, func(t *testing.T) {
			var out bytes.Buffer
			if err := commandeCompletion([]string{shell}, &out); err != nil {
				t.Fatalf("commandeCompletion: %v", err)
			}
			if !strings.Contains(out.String(), marqueur) {
				t.Errorf("le script %s ne contient pas %q :\n%s", shell, marqueur, out.String())
			}
		})
	}
}

func TestCommandeCompletionShellInconnuEstUneErreur(t *testing.T) {
	if err := commandeCompletion([]string{"fish"}, &bytes.Buffer{}); err == nil {
		t.Fatal("aucune erreur pour un shell non supporté")
	}
}

func TestCommandeCompletionSansArgumentEstUneErreur(t *testing.T) {
	if err := commandeCompletion(nil, &bytes.Buffer{}); err == nil {
		t.Fatal("aucune erreur sans argument")
	}
}

// lignes découpe la sortie de `__complete`, un candidat par ligne, et n'en
// garde que le mot — la description qui suit la tabulation est testée à part.
func lignes(t *testing.T, out *bytes.Buffer) []string {
	t.Helper()
	s := strings.TrimRight(out.String(), "\n")
	if s == "" {
		return nil
	}
	l := strings.Split(s, "\n")
	for i := range l {
		l[i], _, _ = strings.Cut(l[i], "\t")
	}
	return l
}

func contient(lignes []string, mot string) bool {
	for _, l := range lignes {
		if l == mot {
			return true
		}
	}
	return false
}

func TestInterneCompleteSansRienTapeListeLesCommandes(t *testing.T) {
	var out bytes.Buffer
	if err := commandeInterneComplete(t.Context(), nil, &out); err != nil {
		t.Fatalf("commandeInterneComplete: %v", err)
	}
	l := lignes(t, &out)
	for _, attendu := range []string{"submit", "login", "host", "completion"} {
		if !contient(l, attendu) {
			t.Errorf("%q absent des candidats : %v", attendu, l)
		}
	}
}

// Un GROUPE (host, user, ...) n'accepte jamais de drapeau avant qu'un verbe
// soit choisi — voir `commandeHote`, qui ne parse les drapeaux qu'à partir du
// second argument. Seuls des verbes ont un sens ici.
func TestInterneCompleteUnGroupeNeProposeQueSesVerbes(t *testing.T) {
	var out bytes.Buffer
	if err := commandeInterneComplete(t.Context(), []string{"host", ""}, &out); err != nil {
		t.Fatalf("commandeInterneComplete: %v", err)
	}
	l := lignes(t, &out)
	for _, attendu := range []string{"declare", "list", "forget"} {
		if !contient(l, attendu) {
			t.Errorf("%q absent des candidats : %v", attendu, l)
		}
	}
	if contient(l, "--env") {
		t.Errorf("un drapeau a été proposé avant le verbe : %v", l)
	}
}

// Une FEUILLE (submit, login, ...) n'a que des drapeaux.
func TestInterneCompleteUneFeuilleNeProposeQueSesDrapeaux(t *testing.T) {
	var out bytes.Buffer
	if err := commandeInterneComplete(t.Context(), []string{"submit", ""}, &out); err != nil {
		t.Fatalf("commandeInterneComplete: %v", err)
	}
	l := lignes(t, &out)
	for _, attendu := range []string{"--env", "--playbook", "--repo", "-b", "-K"} {
		if !contient(l, attendu) {
			t.Errorf("%q absent des candidats : %v", attendu, l)
		}
	}
}

// Le troisième tiroir de la complétion : `host forget` ne propose pas les
// drapeaux de `host declare`.
func TestInterneCompleteUnVerbeNeMontreQueSesPropresDrapeaux(t *testing.T) {
	var out bytes.Buffer
	if err := commandeInterneComplete(t.Context(), []string{"host", "forget", ""}, &out); err != nil {
		t.Fatalf("commandeInterneComplete: %v", err)
	}
	l := lignes(t, &out)
	for _, absent := range []string{"--from-inventory", "-f"} {
		if contient(l, absent) {
			t.Errorf("%q appartient à declare, pas à forget : %v", absent, l)
		}
	}
	if !contient(l, "--env") || !contient(l, "--host") {
		t.Errorf("les drapeaux de forget manquent : %v", l)
	}
}

func TestInterneCompleteFiltreParPrefixe(t *testing.T) {
	var out bytes.Buffer
	if err := commandeInterneComplete(t.Context(), []string{"submit", "--ch"}, &out); err != nil {
		t.Fatalf("commandeInterneComplete: %v", err)
	}
	l := lignes(t, &out)
	if len(l) != 1 || l[0] != "--check" {
		t.Errorf("candidats = %v, attendu [--check]", l)
	}
}

// Sans serveur configuré, la complétion d'une valeur dynamique ne propose
// rien — et surtout ne rend AUCUNE erreur : un Tab n'a pas de sortie standard
// où se plaindre.
func TestInterneCompleteSansServeurNeCraquePas(t *testing.T) {
	t.Chdir(t.TempDir())

	var out bytes.Buffer
	if err := commandeInterneComplete(t.Context(), []string{"submit", "--env", ""}, &out); err != nil {
		t.Fatalf("commandeInterneComplete: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("candidats inattendus sans serveur : %q", out.String())
	}
}

func TestInterneCompleteValeurDynamiqueEnv(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/admin/keys" {
			t.Errorf("chemin inattendu : %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode([]map[string]string{
			{"environment": "prod"},
			{"environment": "staging"},
		})
	}))
	defer srv.Close()
	t.Chdir(t.TempDir())
	t.Setenv(EnvServeur, srv.URL)

	var out bytes.Buffer
	if err := commandeInterneComplete(t.Context(), []string{"submit", "--env", ""}, &out); err != nil {
		t.Fatalf("commandeInterneComplete: %v", err)
	}
	l := lignes(t, &out)
	for _, attendu := range []string{"prod", "staging"} {
		if !contient(l, attendu) {
			t.Errorf("%q absent des candidats : %v", attendu, l)
		}
	}
}

// --host est scopé par --env quand il est déjà posé plus tôt sur la ligne.
func TestInterneCompleteValeurDynamiqueHostScopeeParEnv(t *testing.T) {
	var vuRequete string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		vuRequete = r.URL.String()
		_ = json.NewEncoder(w).Encode([]map[string]string{{"host": "db-01"}})
	}))
	defer srv.Close()
	t.Chdir(t.TempDir())
	t.Setenv(EnvServeur, srv.URL)

	var out bytes.Buffer
	err := commandeInterneComplete(t.Context(),
		[]string{"host", "forget", "--env", "prod", "--host", ""}, &out)
	if err != nil {
		t.Fatalf("commandeInterneComplete: %v", err)
	}
	if !strings.Contains(vuRequete, "environment=prod") {
		t.Errorf("l'environnement n'a pas été transmis : %s", vuRequete)
	}
	if l := lignes(t, &out); !contient(l, "db-01") {
		t.Errorf("db-01 absent des candidats : %v", l)
	}
}

func TestInterneCompleteFlagInconnuNAPasDeSourceEtNeCraquePas(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv(EnvServeur, "http://exemple.invalide")

	var out bytes.Buffer
	err := commandeInterneComplete(t.Context(), []string{"submit", "--playbook", ""}, &out)
	if err != nil {
		t.Fatalf("commandeInterneComplete: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("candidats inattendus pour --playbook : %q", out.String())
	}
}

// Une ligne par verbe : « verbe<TAB>résumé ».
func TestInterneCompleteDecritLesVerbes(t *testing.T) {
	var out bytes.Buffer
	if err := commandeInterneComplete(t.Context(), []string{"host", "li"}, &out); err != nil {
		t.Fatalf("commandeInterneComplete: %v", err)
	}
	if got := out.String(); got != "list\taffiche les machines enrôlées\n" {
		t.Errorf("sortie = %q", got)
	}
}

// Une ligne par drapeau : « --drapeau<TAB>exemple de valeur ».
func TestInterneCompleteDecritLesDrapeauxParUnExemple(t *testing.T) {
	var out bytes.Buffer
	if err := commandeInterneComplete(t.Context(), []string{"run", "all", "--modu"}, &out); err != nil {
		t.Fatalf("commandeInterneComplete: %v", err)
	}
	if got := out.String(); got != "--module\tshell\n" {
		t.Errorf("sortie = %q", got)
	}
}

// Tout drapeau à valeur proposé par la complétion a un exemple : sans lui, la
// ligne retomberait sur le texte d'aide, et l'utilisateur ne saurait pas
// quelle forme de valeur taper.
func TestChaqueDrapeauAValeurAUnExemple(t *testing.T) {
	racine := command.Arbre()
	for _, chemin := range chemins(t) {
		noeud, _ := racine.Trouver(chemin)
		fs := flagsPour(chemin)
		if fs == nil {
			continue
		}
		for _, nom := range noeud.Flags {
			f := fs.Lookup(nom)
			if f == nil || booleen(f) {
				continue
			}
			if noeud.ExempleDeValeur(nom) == "" {
				t.Errorf("warden %s --%s n'a pas d'exemple de valeur",
					strings.Join(chemin, " "), nom)
			}
		}
	}
}

// lancerScript source un script de complétion dans `shell`, avec un faux
// `warden` qui rend `sortie` à `__complete`, puis exécute `appel`. Le script
// est testé contre une sortie fixée, sans le binaire : c'est le format
// « mot<TAB>description » qui est le contrat, pas l'arbre du moment.
func lancerScript(t *testing.T, shell string, script []byte, sortie, appel string) []string {
	t.Helper()
	if _, err := exec.LookPath(shell); err != nil {
		t.Skipf("%s absent : le script %s n'est pas testé sur cette machine", shell, shell)
	}
	dir := t.TempDir()
	ecrire := func(nom, contenu string, mode os.FileMode) string {
		chemin := filepath.Join(dir, nom)
		if err := os.WriteFile(chemin, []byte(contenu), mode); err != nil {
			t.Fatal(err)
		}
		return chemin
	}
	ecrire("sortie", sortie, 0o600)
	ecrire("warden", "#!/bin/sh\ncat \""+filepath.Join(dir, "sortie")+"\"\n", 0o700)
	chemin := ecrire("script", string(script), 0o600)

	cmd := exec.Command(shell, "-c", "source "+chemin+"\n"+appel) // #nosec G204 -- shell fixé par le test
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "LC_ALL=C.UTF-8")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s : %v\n%s", shell, err, out)
	}
	return strings.Split(strings.TrimRight(string(out), "\n"), "\n")
}

const appelBash = `COMP_WORDS=(warden host ""); COMP_CWORD=2; COLUMNS=24
_warden_complete; printf '%s\n' "${COMPREPLY[@]}"`

// Un seul candidat : bash l'insère tel quel, donc sans sa description.
func TestScriptBashCandidatUniqueSansDescription(t *testing.T) {
	l := lancerScript(t, "bash", scriptBash, "list\taffiche les machines\n", appelBash)
	if len(l) != 1 || l[0] != "list" {
		t.Errorf("COMPREPLY = %q, attendu [list]", l)
	}
}

// Plusieurs candidats : « mot  -- description », mots alignés, une ligne par
// candidat — chacune remplie jusqu'à la largeur du terminal, et coupée sans
// trancher un accent.
func TestScriptBashUneLigneParCandidat(t *testing.T) {
	// Le « é » tombe sur la 23e colonne : une coupe en octets le trancherait.
	sortie := "declare\tmachines déjà posées\n" +
		"list\taffiche\n" +
		"prod\n"
	l := lancerScript(t, "bash", scriptBash, sortie, appelBash)
	if len(l) != 3 {
		t.Fatalf("COMPREPLY = %q, attendu 3 candidats", l)
	}
	prefixes := []string{"declare  -- machines d", "list     -- affiche", "prod"}
	for i, ligne := range l {
		if !strings.HasPrefix(ligne, prefixes[i]) {
			t.Errorf("candidat %d = %q, attendu le préfixe %q", i, ligne, prefixes[i])
		}
		if !utf8.ValidString(ligne) {
			t.Errorf("candidat %d coupé dans un caractère : %q", i, ligne)
		}
		if n := utf8.RuneCountInString(ligne); n > 23 {
			t.Errorf("candidat %d dépasse la largeur : %d caractères", i, n)
		}
	}
	if utf8.RuneCountInString(l[1]) != 23 {
		t.Errorf("candidat %q non rempli jusqu'à la largeur du terminal", l[1])
	}
}

// zsh n'est pas forcément installé : sans lui, le test est sauté, et le dit.
// `compdef`, `_describe` et `_files` viennent de compinit, qu'un shell non
// interactif n'a pas : ils sont remplacés par des doublures qui impriment ce
// qu'on leur passe.
const appelZsh = `_describe() { print -rl -- "${(@P)${@[-1]}}" }
_files() { print -r -- _files }
words=(warden host ""); CURRENT=3
__warden_complete`

func lancerScriptZsh(t *testing.T, sortie string) []string {
	t.Helper()
	return lancerScript(t, "zsh", append([]byte("compdef() { : }\n"), scriptZsh...), sortie, appelZsh)
}

func TestScriptZshPasseMotEtDescriptionADescribe(t *testing.T) {
	l := lancerScriptZsh(t, "list\taffiche\nweb:8080\tun port\nprod\n")
	attendus := []string{"list:affiche", `web\:8080:un port`, "prod"}
	if strings.Join(l, "\n") != strings.Join(attendus, "\n") {
		t.Errorf("candidats = %q, attendu %q", l, attendus)
	}
}

func TestScriptZshSansCandidatProposeLesFichiers(t *testing.T) {
	if l := lancerScriptZsh(t, ""); len(l) != 1 || l[0] != "_files" {
		t.Errorf("sortie = %q, attendu [_files]", l)
	}
}

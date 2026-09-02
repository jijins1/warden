package admin_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	"github.com/jijins1/warden/internal/cli/internal/admin"
	"github.com/jijins1/warden/internal/cli/internal/client"
)

// depotAvecRemote monte un dépôt git jetable et lui donne un remote.
//
// Un VRAI dépôt et un vrai binaire git, comme le paquet `git` le fait déjà dans
// ses propres tests : ce qu'on vérifie ici est la lecture d'une configuration
// git, et un faux `git remote get-url` ne vérifierait que le faux.
func depotAvecRemote(t *testing.T, url string) string {
	t.Helper()
	dir := t.TempDir()
	lancer(t, dir, "init", "--initial-branch=main")
	lancer(t, dir, "config", "user.email", "test@example.com")
	lancer(t, dir, "config", "user.name", "Test")
	if url != "" {
		lancer(t, dir, "remote", "add", "origin", url)
	}
	return dir
}

func lancer(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir
	if sortie, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, sortie)
	}
}

// Le cas nominal : rien n'est donné, tout est déduit.
func TestDetecterDeduitToutDuDepot(t *testing.T) {
	dir := depotAvecRemote(t, "https://forge.example.com/equipe/ansible.git")

	r, err := admin.Detecter(t.Context(), admin.OptionsDepot{RepoDir: dir})
	if err != nil {
		t.Fatalf("Detecter: %v", err)
	}
	if r.Name != "ansible" {
		t.Errorf("nom = %q, attendu ansible", r.Name)
	}
	if r.RemoteURL != "https://forge.example.com/equipe/ansible.git" {
		t.Errorf("remote = %q", r.RemoteURL)
	}
	// Le remote n'annonce pas de branche par défaut — rien n'a été cloné — donc
	// c'est la branche courante qui sert de repli. C'est précisément pour ce cas
	// que la commande fait CONFIRMER ce qu'elle a trouvé.
	if r.MainlineRef != "refs/heads/main" {
		t.Errorf("mainline_ref = %q, attendu refs/heads/main", r.MainlineRef)
	}
}

// La branche COURANTE sert de repli, et c'est le piège que la confirmation
// existe pour rattraper : déclarer depuis une branche de feature épinglerait le
// mauvais point de comparaison pour tous les runs du dépôt.
func TestDetecterRetombeSurLaBrancheCourante(t *testing.T) {
	dir := depotAvecRemote(t, "https://forge.example.com/equipe/ansible.git")
	lancer(t, dir, "checkout", "-b", "ma-feature")

	r, err := admin.Detecter(t.Context(), admin.OptionsDepot{RepoDir: dir})
	if err != nil {
		t.Fatalf("Detecter: %v", err)
	}
	if r.MainlineRef != "refs/heads/ma-feature" {
		t.Errorf("mainline_ref = %q, attendu refs/heads/ma-feature", r.MainlineRef)
	}
}

// Rien de ce qui est donné n'est déduit : les drapeaux l'emportent toujours.
func TestDetecterNEcrasePasCeQuOnDonne(t *testing.T) {
	dir := depotAvecRemote(t, "https://forge.example.com/equipe/ansible.git")

	r, err := admin.Detecter(t.Context(), admin.OptionsDepot{
		RepoDir:     dir,
		Name:        "autre-nom",
		RemoteURL:   "https://ailleurs.example.com/x.git",
		MainlineRef: "refs/heads/production",
	})
	if err != nil {
		t.Fatalf("Detecter: %v", err)
	}
	if r.Name != "autre-nom" || r.RemoteURL != "https://ailleurs.example.com/x.git" ||
		r.MainlineRef != "refs/heads/production" {
		t.Errorf("descripteur = %+v", r)
	}
}

// Une URL SSH est le cas le plus fréquent d'un `origin` de développeur, et le
// serveur ne sait cloner qu'en http(s). Le refus doit dire quoi faire.
func TestDetecterRefuseUneURLNonHTTP(t *testing.T) {
	for _, url := range []string{
		"git@forge.example.com:equipe/ansible.git",
		"ssh://git@forge.example.com/equipe/ansible.git",
		"/chemin/local/ansible.git",
	} {
		t.Run(url, func(t *testing.T) {
			dir := depotAvecRemote(t, url)
			_, err := admin.Detecter(t.Context(), admin.OptionsDepot{RepoDir: dir})
			if err == nil {
				t.Fatalf("URL %q acceptée", url)
			}
			if !strings.Contains(err.Error(), "--remote-url") {
				t.Errorf("l'erreur ne dit pas quoi faire : %v", err)
			}
		})
	}
}

// Un dépôt sans `origin` : l'erreur nomme le remote cherché plutôt que de
// laisser remonter un code de sortie de git.
func TestDetecterSansRemote(t *testing.T) {
	dir := depotAvecRemote(t, "")

	_, err := admin.Detecter(t.Context(), admin.OptionsDepot{RepoDir: dir})
	if err == nil {
		t.Fatal("un dépôt sans remote a été accepté")
	}
	if !strings.Contains(err.Error(), "origin") {
		t.Errorf("l'erreur ne nomme pas le remote : %v", err)
	}
}

// --remote vise un autre remote que `origin`.
func TestDetecterSuitLeRemoteDemande(t *testing.T) {
	dir := depotAvecRemote(t, "https://forge.example.com/equipe/ansible.git")
	lancer(t, dir, "remote", "add", "amont", "https://amont.example.com/socle/ansible-socle.git")

	r, err := admin.Detecter(t.Context(), admin.OptionsDepot{RepoDir: dir, Remote: "amont"})
	if err != nil {
		t.Fatalf("Detecter: %v", err)
	}
	if r.Name != "ansible-socle" {
		t.Errorf("nom = %q, attendu ansible-socle", r.Name)
	}
}

// Hors d'un dépôt git, la commande n'a rien à déduire et doit le dire.
func TestDetecterHorsDUnDepotGit(t *testing.T) {
	_, err := admin.Detecter(t.Context(), admin.OptionsDepot{RepoDir: t.TempDir()})
	if err == nil {
		t.Fatal("un répertoire quelconque a été accepté")
	}
	if !strings.Contains(err.Error(), "dépôt git") {
		t.Errorf("l'erreur n'explique pas : %v", err)
	}
}

// Le nom est DÉCOUPÉ d'une URL, donc personne ne l'a tapé : il peut être
// n'importe quoi, et la contrainte du schéma serveur s'applique ici aussi.
func TestDetecterRefuseUnNomQueLeSchemaRefuserait(t *testing.T) {
	dir := depotAvecRemote(t, "https://forge.example.com/equipe/-mauvais.git")

	_, err := admin.Detecter(t.Context(), admin.OptionsDepot{RepoDir: dir})
	if err == nil {
		t.Fatal("un nom commençant par un tiret a été accepté")
	}
	if !strings.Contains(err.Error(), "--name") {
		t.Errorf("l'erreur ne dit pas quoi faire : %v", err)
	}
}

// Une ref de branche est attendue : `main` tout court serait accepté par la CLI
// et refusé par la base, après un aller-retour.
func TestDetecterRefuseUneMainlineQuiNEstPasUneRef(t *testing.T) {
	dir := depotAvecRemote(t, "https://forge.example.com/equipe/ansible.git")

	_, err := admin.Detecter(t.Context(), admin.OptionsDepot{
		RepoDir: dir, MainlineRef: "main",
	})
	if err == nil {
		t.Fatal("« main » a été accepté comme mainline_ref")
	}
	if !strings.Contains(err.Error(), "refs/heads/") {
		t.Errorf("l'erreur ne donne pas la forme attendue : %v", err)
	}
}

// La branche par défaut du REMOTE l'emporte sur la branche courante.
//
// L'ordre porte la correction : la branche courante est ce qu'on a sous la main,
// pas ce contre quoi les runs doivent être comparés. Un clone pose
// `refs/remotes/origin/HEAD`, et c'est lui qui fait autorité quand il existe.
func TestDetecterPrefereLaBrancheParDefautDuRemote(t *testing.T) {
	amont := t.TempDir()
	lancer(t, amont, "init", "--initial-branch=principale")
	lancer(t, amont, "config", "user.email", "test@example.com")
	lancer(t, amont, "config", "user.name", "Test")
	lancer(t, amont, "commit", "--allow-empty", "-m", "un")

	clone := t.TempDir()
	lancer(t, "", "clone", amont, clone)
	// Le clone pose bien `origin/HEAD`, mais son URL est un chemin local que le
	// serveur ne saurait pas cloner. On la remplace par la forme http, ce qui
	// laisse `refs/remotes/origin/HEAD` intact — c'est lui qu'on teste.
	lancer(t, clone, "remote", "set-url", "origin", "https://forge.example.com/equipe/ansible.git")
	lancer(t, clone, "checkout", "-b", "ma-feature")

	r, err := admin.Detecter(t.Context(), admin.OptionsDepot{RepoDir: clone})
	if err != nil {
		t.Fatalf("Detecter: %v", err)
	}
	if r.MainlineRef != "refs/heads/principale" {
		t.Errorf("mainline_ref = %q, attendu refs/heads/principale "+
			"(la branche du remote, pas ma-feature)", r.MainlineRef)
	}
}

// Un refus de confirmation n'écrit RIEN. C'est le point de la question : elle
// est posée parce que `mainline_ref` peut avoir été déduit d'une branche de
// feature, et une question dont la réponse ne change rien serait pire qu'aucune.
func TestDeclarerDepotRefuseNAppellePasLeServeur(t *testing.T) {
	dir := depotAvecRemote(t, "https://forge.example.com/equipe/ansible.git")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("le serveur a été appelé malgré le refus")
	}))
	defer srv.Close()

	api, err := client.New(client.Config{BaseURL: srv.URL, Subject: "alice"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var sortie bytes.Buffer
	err = admin.DeclarerDepot(t.Context(), api, admin.OptionsDepot{
		RepoDir:   dir,
		Confirmer: func(client.Repo) bool { return false },
	}, &sortie)
	if err == nil {
		t.Fatal("un refus a rendu une réussite")
	}
	// Le descripteur est affiché AVANT la question : sans ça, l'utilisateur
	// répondrait sans savoir à quoi.
	if !strings.Contains(sortie.String(), "refs/heads/main") {
		t.Errorf("le descripteur n'a pas été affiché :\n%s", sortie.String())
	}
}

// Le chemin nominal, jusqu'à l'écriture, avec la ligne à coller en sortie.
func TestDeclarerDepotEcritEtDonneLaLigneAColler(t *testing.T) {
	dir := depotAvecRemote(t, "https://forge.example.com/equipe/ansible.git")

	var vuChemin string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		vuChemin = r.URL.Path
	}))
	defer srv.Close()

	api, err := client.New(client.Config{BaseURL: srv.URL, Subject: "alice"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var sortie bytes.Buffer
	// Confirmer nul : c'est ce que `--yes` produit.
	if err := admin.DeclarerDepot(t.Context(), api,
		admin.OptionsDepot{RepoDir: dir}, &sortie); err != nil {
		t.Fatalf("DeclarerDepot: %v", err)
	}

	if vuChemin != "/admin/repos/ansible" {
		t.Errorf("chemin = %q", vuChemin)
	}
	if !strings.Contains(sortie.String(), "repo: ansible") {
		t.Errorf("la ligne à coller manque :\n%s", sortie.String())
	}
	// La ligne ne sert à rien tant que le serveur refuse le champ : le dire ici
	// évite que quelqu'un la commite et casse tous les runs du dépôt.
	if !strings.Contains(sortie.String(), "SERVEUR") {
		t.Errorf("l'ordre de déploiement n'est pas dit :\n%s", sortie.String())
	}
}

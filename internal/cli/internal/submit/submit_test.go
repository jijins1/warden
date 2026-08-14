// Étage 1 de l'ADR-0010 : un vrai dépôt, un vrai `git http-backend`, une API
// factice.
//
// L'ADR-0011 interdit à la CLI d'importer le control plane — deux domaines de
// confiance — donc le rôle `git` ne peut pas être monté ici. Ce que ces tests
// établissent est ce qui relève de CE côté de la frontière : que la séquence du
// §5.2 est respectée, que la ref poussée est celle que le serveur accordera, et
// que le dépôt de l'utilisateur n'est pas touché. Le comportement du hook est
// couvert par les tests du rôle `git`.

package submit_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jijins1/warden/internal/cli/internal/client"
	"github.com/jijins1/warden/internal/cli/internal/submit"
)

func gitBin(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git absent du PATH : %v", err)
	}
	return p
}

func lancerGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), gitBin(t), args...) // #nosec G204 -- arguments littéraux du test
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=/usr/bin:/bin", "HOME=" + dir,
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0", "LC_ALL=C",
		"GIT_AUTHOR_NAME=w", "GIT_AUTHOR_EMAIL=w@invalid",
		"GIT_COMMITTER_NAME=w", "GIT_COMMITTER_EMAIL=w@invalid",
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s : %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// serveurGit sert un dépôt bare en écriture, et enregistre l'autorisation vue.
func serveurGit(t *testing.T, bare string) (string, *string) {
	t.Helper()
	vu := new(string)

	backend := &cgi.Handler{
		Path: gitBin(t),
		Args: []string{"http-backend"},
		Dir:  filepath.Dir(bare),
		Env: []string{
			"GIT_PROJECT_ROOT=" + filepath.Dir(bare),
			"GIT_HTTP_EXPORT_ALL=1", "HOME=" + filepath.Dir(bare),
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
			"PATH=/usr/bin:/bin",
			"GIT_COMMITTER_NAME=w", "GIT_COMMITTER_EMAIL=w@invalid",
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=http.receivepack", "GIT_CONFIG_VALUE_0=true",
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a := r.Header.Get("Authorization"); a != "" {
			*vu = a
		}
		backend.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/" + filepath.Base(bare), vu
}

// apiFactice répond comme le control plane, et retient ce qu'on lui a envoyé.
type apiFactice struct {
	prefix string
	recus  []client.NewRun
	erreur error
}

func (a *apiFactice) IssuePushGrant(context.Context, string) (client.Grant, error) {
	return client.Grant{Token: "jeton-de-push", RefPrefix: a.prefix}, nil
}

func (a *apiFactice) CreateRun(_ context.Context, n client.NewRun) (client.RunCreated, error) {
	if a.erreur != nil {
		return client.RunCreated{}, a.erreur
	}
	a.recus = append(a.recus, n)
	return client.RunCreated{RunRef: n.RunRef, AuditSHA: "resolu-par-le-serveur"}, nil
}

// monde construit un dépôt utilisateur avec du travail non commité, et le dépôt
// bare qui le reçoit.
func monde(t *testing.T) (repoDir, bare, gitURL string, vu *string) {
	t.Helper()

	racine := t.TempDir()
	bare = filepath.Join(racine, "depot.git")
	lancerGit(t, racine, "init", "--bare", "--initial-branch=main", bare)

	repoDir = t.TempDir()
	lancerGit(t, repoDir, "init", "--initial-branch=main", ".")
	ecrire(t, repoDir, "site.yml", "- hosts: all")
	lancerGit(t, repoDir, "add", ".")
	lancerGit(t, repoDir, "commit", "-m", "base")

	gitURL, vu = serveurGit(t, bare)
	return repoDir, bare, gitURL, vu
}

func ecrire(t *testing.T, dir, nom, contenu string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, nom), []byte(contenu), 0o600); err != nil {
		t.Fatalf("écriture : %v", err)
	}
}

func options(repoDir, gitURL string) submit.Options {
	return submit.Options{
		RepoDir: repoDir, Repo: "depot", Environment: "prod",
		Inventory: "inv", Playbook: "site.yml", GitURL: gitURL,
	}
}

// TestLaSequenceComplete est le chemin nominal de bout en bout.
func TestLaSequenceComplete(t *testing.T) {
	repoDir, bare, gitURL, vu := monde(t)

	// Du travail non commité : c'est précisément ce que l'audit doit capturer et
	// que le dépôt de l'utilisateur ne doit pas perdre.
	ecrire(t, repoDir, "inventory.ini", "[prod]\nweb-01")

	api := &apiFactice{prefix: "refs/audit/7/"}
	res, err := submit.Run(t.Context(), api, options(repoDir, gitURL))
	if err != nil {
		t.Fatalf("submit.Run : %v", err)
	}

	// La ref poussée est celle que le serveur accordera : son préfixe, notre
	// identifiant.
	attendue := "refs/audit/7/" + res.RunRef
	if out := lancerGit(t, bare, "rev-parse", "--verify", attendue); strings.TrimSpace(out) == "" {
		t.Fatalf("la ref %s n'existe pas côté serveur", attendue)
	}

	// Le jeton voyage dans un en-tête, jamais dans l'URL ni dans argv.
	if !strings.Contains(*vu, "jeton-de-push") {
		t.Errorf("le jeton n'a pas traversé le transport : %q", *vu)
	}

	if len(api.recus) != 1 {
		t.Fatalf("%d run(s) déclaré(s)", len(api.recus))
	}
	// Le commit d'audit n'est PAS déclaré : le serveur le résout lui-même.
	if strings.Contains(string(mustJSON(t, api.recus[0])), "audit_sha") {
		t.Error("la CLI déclare un audit_sha, que le serveur refuse par conception")
	}
}

// TestLeDepotDeLUtilisateurNestPasTouche est l'exigence non négociable
// d'adoption (décision actée n°4).
//
// Toute la mécanique de capture existe pour cela : un produit d'audit qui
// bricole le dépôt de son utilisateur ne sera pas adopté, quelles que soient ses
// autres qualités.
func TestLeDepotDeLUtilisateurNestPasTouche(t *testing.T) {
	repoDir, _, gitURL, _ := monde(t)
	ecrire(t, repoDir, "inventory.ini", "[prod]\nweb-01")

	avantHead := strings.TrimSpace(lancerGit(t, repoDir, "rev-parse", "HEAD"))
	avantStatut := lancerGit(t, repoDir, "status", "--porcelain")
	avantRefs := lancerGit(t, repoDir, "for-each-ref", "--format=%(refname) %(objectname)")

	if _, err := submit.Run(t.Context(), &apiFactice{prefix: "refs/audit/7/"},
		options(repoDir, gitURL)); err != nil {
		t.Fatalf("submit.Run : %v", err)
	}

	if got := strings.TrimSpace(lancerGit(t, repoDir, "rev-parse", "HEAD")); got != avantHead {
		t.Errorf("HEAD a bougé : %s -> %s", avantHead, got)
	}
	if got := lancerGit(t, repoDir, "status", "--porcelain"); got != avantStatut {
		t.Errorf("l'état de travail a changé :\n%s\n-->\n%s", avantStatut, got)
	}
	if got := lancerGit(t, repoDir, "for-each-ref", "--format=%(refname) %(objectname)"); got != avantRefs {
		t.Errorf("les refs ont changé :\n%s\n-->\n%s", avantRefs, got)
	}
}

// TestUnEchecApresLePushLeDitPrecisement.
//
// La ref est poussée, le run n'est pas déclaré. Le message doit nommer les deux
// faits ET l'identifiant, sans quoi l'utilisateur croit que rien ne s'est passé
// et le support cherche un artefact qu'il ne sait pas nommer.
func TestUnEchecApresLePushLeDitPrecisement(t *testing.T) {
	repoDir, _, gitURL, _ := monde(t)

	api := &apiFactice{prefix: "refs/audit/7/", erreur: errors.New("base indisponible")}
	_, err := submit.Run(t.Context(), api, options(repoDir, gitURL))
	if err == nil {
		t.Fatal("erreur attendue")
	}
	for _, attendu := range []string{"poussé", "n'a pas été déclaré"} {
		if !strings.Contains(err.Error(), attendu) {
			t.Errorf("le message ne dit pas %q : %v", attendu, err)
		}
	}
}

// TestUnePorteeInutilisableEstRefuseeAvantToutTravail : échouer avant de
// capturer plutôt qu'après avoir écrit des objets pour rien.
func TestUnePorteeInutilisableEstRefuseeAvantToutTravail(t *testing.T) {
	repoDir, _, gitURL, _ := monde(t)

	for _, prefixe := range []string{"", "pas-une-ref/", "refs/audit/../../etc/"} {
		t.Run(prefixe, func(t *testing.T) {
			_, err := submit.Run(t.Context(), &apiFactice{prefix: prefixe},
				options(repoDir, gitURL))
			if err == nil {
				t.Fatal("un préfixe inutilisable a été accepté")
			}
		})
	}
}

// TestUneConfigurationIncompleteLeDitEnUneFois : §5.7 — un message d'erreur doit
// dire ce qu'il faut faire, et tout ce qu'il faut faire.
func TestUneConfigurationIncompleteLeDitEnUneFois(t *testing.T) {
	_, err := submit.Run(t.Context(), &apiFactice{prefix: "refs/audit/7/"}, submit.Options{})
	if err == nil {
		t.Fatal("erreur attendue")
	}
	for _, attendu := range []string{"dépôt", "playbook", "git"} {
		if !strings.Contains(err.Error(), attendu) {
			t.Errorf("le message ne nomme pas %q : %v", attendu, err)
		}
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json : %v", err)
	}
	return b
}

// TestRejouerUneSoumissionEstIdempotent : la propriété que la reprise après
// panne suppose.
//
// Un échec entre le push et la déclaration du run laisse une ref d'audit
// orpheline. Rejouer avec le MÊME identifiant doit reproduire exactement le même
// commit — sinon la reprise crée un second enregistrement du même travail, et
// l'écriture unique de la ref (ADR-0027) fait échouer le rejeu au lieu de le
// laisser passer.
func TestRejouerUneSoumissionEstIdempotent(t *testing.T) {
	repoDir, bare, gitURL, _ := monde(t)
	ecrire(t, repoDir, "inventory.ini", "[prod]\nweb-01")

	// Premier essai : le push réussit, la déclaration échoue.
	casse := &apiFactice{prefix: "refs/audit/7/", erreur: errors.New("base indisponible")}
	opts := options(repoDir, gitURL)
	opts.RunRef = "01JZQ000000000000000000ABC"

	if _, err := submit.Run(t.Context(), casse, opts); err == nil {
		t.Fatal("échec attendu à la déclaration")
	}
	pousse := strings.TrimSpace(lancerGit(t, bare, "rev-parse", "--verify", "refs/audit/7/"+opts.RunRef))

	// Reprise avec le même identifiant : même commit, ref inchangée, et le
	// serveur n'a rien à refuser.
	res, err := submit.Run(t.Context(), &apiFactice{prefix: "refs/audit/7/"}, opts)
	if err != nil {
		t.Fatalf("la reprise a échoué : %v", err)
	}
	if res.RunRef != opts.RunRef {
		t.Errorf("la reprise a tiré un nouvel identifiant : %s", res.RunRef)
	}

	apres := strings.TrimSpace(lancerGit(t, bare, "rev-parse", "--verify", "refs/audit/7/"+opts.RunRef))
	if apres != pousse {
		t.Errorf("le rejeu a produit un commit différent : %s -> %s", pousse, apres)
	}
}

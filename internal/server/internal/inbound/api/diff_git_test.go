//go:build integration

// Étage 2 de l'ADR-0010 : PostgreSQL réel par testcontainers, plus le rôle `git`
// monté en process devant un dépôt bare temporaire.
//
// C'est le seul test qui prouve la chaîne complète api → git, et donc la
// sémantique corrigée du §6.5 de bout en bout. L'ADR-0006 autorise explicitement
// les trois rôles dans un même process en développement et en CI.

package api_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jijins1/warden/internal/server/internal/core"
	"github.com/jijins1/warden/internal/server/internal/gitd"
	"github.com/jijins1/warden/internal/server/internal/inbound/api"
	"github.com/jijins1/warden/internal/server/internal/outbound/gitclient"
	"github.com/jijins1/warden/internal/server/internal/outbound/store"
)

const jetonGit = "jeton-interne-de-test"

// TestDiffGitBoutEnBout monte le rôle `git` derrière l'API et vérifie les deux
// vues du §6.5 sur un vrai dépôt.
func TestDiffGitBoutEnBout(t *testing.T) {
	h := setup(t)
	ctx := context.Background()

	racine, shas := depotDeTest(t)
	h.brancherRoleGit(t, racine)

	// Deux runs successifs sur le même triplet, aux commits successifs du dépôt.
	ref1 := h.creerRunGit(t, ctx, h.userID, "P01", shas[0])
	ref2 := h.creerRunGit(t, ctx, h.userID, "P02", shas[1])
	_ = ref1

	t.Run("vs=mainline", func(t *testing.T) {
		resp := h.get(t, "/runs/"+ref2+"/diff?vs=mainline", h.userSubject)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("statut %d, attendu 200", resp.StatusCode)
		}
		corps, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(corps), "deuxieme") {
			t.Errorf("diff mainline inattendu:\n%s", corps)
		}
	})

	t.Run("vs=previous", func(t *testing.T) {
		resp := h.get(t, "/runs/"+ref2+"/diff?vs=previous", h.userSubject)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("statut %d, attendu 200", resp.StatusCode)
		}
		corps, _ := io.ReadAll(resp.Body)
		// Le diff porte sur audit_sha du run précédent -> audit_sha de celui-ci,
		// soit exactement l'écart entre les deux commits.
		if !strings.Contains(string(corps), "deuxieme") {
			t.Errorf("diff previous inattendu:\n%s", corps)
		}
	})

	t.Run("premier run sans précédent", func(t *testing.T) {
		resp := h.get(t, "/runs/"+ref1+"/diff?vs=previous", h.userSubject)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("statut %d, attendu 404", resp.StatusCode)
		}
		if m := resp.Header.Get("X-Warden-Reason"); m != "no_previous_run" {
			t.Errorf("motif %q", m)
		}
	})
}

// TestDiffVsCurrent vérifie que vs=current compare au sommet ACTUEL de la
// branche protégée dans le miroir — pas au mainline_sha figé dans le run — en
// avançant ce sommet APRÈS la création du run et en vérifiant que le diff
// rendu change en conséquence.
func TestDiffVsCurrent(t *testing.T) {
	h := setup(t)
	ctx := context.Background()

	racine, shas := depotDeTest(t)
	h.brancherRoleGit(t, racine)

	if err := h.store.UpsertRepo(ctx, core.Repo{
		Name: "depot", RemoteURL: "https://example.invalid/depot.git",
		MainlineRef: "refs/heads/principale",
	}, h.userID); err != nil {
		t.Fatalf("UpsertRepo: %v", err)
	}

	ref := h.creerRunGit(t, ctx, h.userID, "P03", shas[1])

	// Sans rien avancer : audit_sha du run EST déjà le sommet de la branche,
	// donc vs=current ne montre rien de plus que vs=mainline ne montrerait pas
	// déjà — le diff est vide.
	resp := h.get(t, "/runs/"+ref+"/diff?vs=current", h.userSubject)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("statut %d, attendu 200", resp.StatusCode)
	}
	corps, _ := io.ReadAll(resp.Body)
	if string(corps) != "" {
		t.Errorf("diff non vide avant toute avance de la branche :\n%s", corps)
	}

	// La branche avance APRÈS le run — exactement ce que "le playbook actuel"
	// doit voir et que mainline_sha, figé à la création, ne peut pas.
	avancerBranche(t, racine, "troisieme")

	resp = h.get(t, "/runs/"+ref+"/diff?vs=current", h.userSubject)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("statut %d, attendu 200", resp.StatusCode)
	}
	corps, _ = io.ReadAll(resp.Body)
	if !strings.Contains(string(corps), "troisieme") {
		t.Errorf("diff vs=current inattendu, devrait montrer l'avance de la branche:\n%s", corps)
	}
}

// avancerBranche ajoute un commit sur `refs/heads/principale` du dépôt bare
// `racine/depot.git`, en clonant puis en repoussant — ce que ResolveRef lit
// ensuite SANS relecture réseau, puisque c'est le même fichier sur disque que
// le miroir que gitd sert.
func avancerBranche(t *testing.T, racine, ligne string) {
	t.Helper()
	clone := t.TempDir()

	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}

	git("", "clone", filepath.Join(racine, "depot.git"), clone)
	git(clone, "config", "user.email", "test@example.com")
	git(clone, "config", "user.name", "Test")

	f := filepath.Join(clone, "f.txt")
	contenu, err := os.ReadFile(f)
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	if err := os.WriteFile(f, append(contenu, []byte(ligne+"\n")...), 0o600); err != nil {
		t.Fatalf("écriture: %v", err)
	}
	git(clone, "add", "f.txt")
	git(clone, "commit", "-m", "trois")
	git(clone, "push", "origin", "principale")
}

// TestDiffPrecedentDUnAutreUtilisateurRefuse est LA propriété de sécurité de
// cette tranche.
//
// Le run précédent sur un inventaire appartient souvent à quelqu'un d'autre. Le
// servir sans contrôle exposerait SON arbre — son code — à un appelant qui n'a
// pas le droit de voir ce run. requireRunAccess ne vérifie que le run demandé.
func TestDiffPrecedentDUnAutreUtilisateurRefuse(t *testing.T) {
	h := setup(t)
	ctx := context.Background()

	racine, shas := depotDeTest(t)
	h.brancherRoleGit(t, racine)

	// Mallory a joué en premier sur cet inventaire.
	mallory, err := h.store.UpsertUser(ctx, core.Identity{
		Type: core.UserLocal, Subject: "mallory", DisplayName: "Mallory",
	})
	if err != nil {
		t.Fatalf("UpsertUser: %v", err)
	}
	h.creerRunGit(t, ctx, mallory.ID, "M01", shas[0])

	// Puis l'utilisateur du harnais.
	ref := h.creerRunGit(t, ctx, h.userID, "P02", shas[1])

	resp := h.get(t, "/runs/"+ref+"/diff?vs=previous", h.userSubject)

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("statut %d, attendu 404 : le run précédent appartient à un autre utilisateur", resp.StatusCode)
	}
	// Le refus se présente comme une absence : distinguer « interdit » de
	// « inexistant » ferait de l'endpoint un oracle.
	if m := resp.Header.Get("X-Warden-Reason"); m != "no_previous_run" {
		t.Errorf("motif %q, attendu no_previous_run", m)
	}
	corps, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(corps), "deuxieme") || strings.Contains(string(corps), "diff") {
		t.Errorf("le corps laisse fuir du contenu:\n%s", corps)
	}
}

// TestDiffSansRoleGit : sans client, l'endpoint refuse plutôt que de lire un
// dépôt local — le rôle `api` n'a jamais accès au filesystem git (ADR-0006).
func TestDiffSansRoleGit(t *testing.T) {
	h := setup(t) // Deps.Repo reste nil
	ctx := context.Background()

	_, shas := depotDeTest(t)
	ref := h.creerRunGit(t, ctx, h.userID, "P01", shas[0])

	resp := h.get(t, "/runs/"+ref+"/diff?vs=mainline", h.userSubject)
	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("statut %d, attendu 501", resp.StatusCode)
	}
}

// brancherRoleGit monte gitd derrière un httptest.Server et câble le client sur
// le serveur d'API du harnais — le même assemblage que cmd/wardend.
func (h *harness) brancherRoleGit(t *testing.T, racine string) {
	t.Helper()

	g, err := gitd.New(gitd.Config{Racine: racine, Jeton: jetonGit})
	if err != nil {
		t.Fatalf("gitd.New: %v", err)
	}
	srvGit := httptest.NewServer(g.Handler())
	t.Cleanup(srvGit.Close)

	gc, err := gitclient.New(gitclient.Config{BaseURL: srvGit.URL, Token: jetonGit})
	if err != nil {
		t.Fatalf("gitclient.New: %v", err)
	}

	h.remonter(t, api.Deps{
		Runs: h.store, Users: h.store, Diffs: h.diffs, Events: h.flux, Repo: gc,
		Repos: h.store, Refs: gc,
	})
}

// creerRunGit insère un run pointant sur un commit réel du dépôt de test.
func (h *harness) creerRunGit(t *testing.T, ctx context.Context, userID int64, suffixe, auditSHA string) string {
	t.Helper()

	ref := "01JZQ" + strings.Repeat("0", 26-5-len(suffixe)) + suffixe
	_, err := h.store.CreateRun(ctx, store.NewRun{
		RunRef: ref, UserID: userID, Repo: "depot",
		Environment: "prod", Inventory: "inv", Playbook: "p.yml",
		AuditSHA:    auditSHA,
		BaseSHA:     "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		MainlineRef: "refs/heads/principale",
		// La mainline est figée sur le PREMIER commit : le diff « run contre
		// branche principale » se calcule contre cette position, pas contre la
		// branche d'aujourd'hui (ADR-0021).
		MainlineSHA: premierCommit,
	})
	if err != nil {
		t.Fatalf("CreateRun %s: %v", ref, err)
	}
	return ref
}

// premierCommit est renseigné par depotDeTest.
var premierCommit string

// depotDeTest crée un dépôt bare de deux commits sous une racine temporaire.
func depotDeTest(t *testing.T) (string, []string) {
	t.Helper()

	racine := t.TempDir()
	travail := t.TempDir()

	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", args...)
		if dir != "" {
			cmd.Dir = dir
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	head := func(dir string) string {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", "rev-parse", "HEAD")
		cmd.Dir = dir
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("rev-parse: %v", err)
		}
		return strings.TrimSpace(string(out))
	}

	git(travail, "init", "--initial-branch=principale")
	git(travail, "config", "user.email", "test@example.com")
	git(travail, "config", "user.name", "Test")

	if err := os.WriteFile(filepath.Join(travail, "f.txt"), []byte("premiere\n"), 0o600); err != nil {
		t.Fatalf("écriture: %v", err)
	}
	git(travail, "add", "f.txt")
	git(travail, "commit", "-m", "un")
	un := head(travail)

	if err := os.WriteFile(filepath.Join(travail, "f.txt"), []byte("premiere\ndeuxieme\n"), 0o600); err != nil {
		t.Fatalf("écriture: %v", err)
	}
	git(travail, "add", "f.txt")
	git(travail, "commit", "-m", "deux")
	deux := head(travail)

	git("", "clone", "--bare", travail, filepath.Join(racine, "depot.git"))

	premierCommit = un
	return racine, []string{un, deux}
}

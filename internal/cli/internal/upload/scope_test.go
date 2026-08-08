package upload

import (
	"context"
	"strings"
	"testing"
)

// depotPerimetre construit un dépôt où chaque catégorie du §5.3 est représentée :
// un fichier suivi, un non suivi non ignoré, un non suivi ignoré par git.
func depotPerimetre(t *testing.T) string {
	t.Helper()

	dir := depot(t)
	ecrire(t, dir, ".gitignore", "inventaire.ini\nsecrets/\n")
	ecrire(t, dir, "site.yml", "- hosts: all\n")
	lancer(t, dir, "add", "-A")
	lancer(t, dir, "commit", "-m", "base")

	ecrire(t, dir, "roles/web/tasks/main.yml", "- name: rien\n") // non suivi, non ignoré
	ecrire(t, dir, "inventaire.ini", "[web]\nweb-01\n")          // non suivi, ignoré
	ecrire(t, dir, "secrets/vault.yml", "$ANSIBLE_VAULT;1.1\n")  // non suivi, ignoré
	return dir
}

// TestPerimetreParDefaut : suivis + non suivis non ignorés, et rien d'autre.
func TestPerimetreParDefaut(t *testing.T) {
	dir := depotPerimetre(t)

	p, err := DetermineScope(context.Background(), dir, Options{})
	if err != nil {
		t.Fatalf("DetermineScope: %v", err)
	}

	fichiers := p.Files()
	for _, attendu := range []string{"site.yml", ".gitignore", "roles/web/tasks/main.yml"} {
		if !contient(fichiers, attendu) {
			t.Errorf("%s absent du périmètre: %v", attendu, fichiers)
		}
	}
	for _, refuse := range []string{"inventaire.ini", "secrets/vault.yml"} {
		if contient(fichiers, refuse) {
			t.Errorf("%s ignoré par git mais présent dans le périmètre: %v", refuse, fichiers)
		}
	}
}

// TestAuditIgnoreRemplaceGitignore est la raison d'être du §5.3.
//
// « Le .gitignore du projet exclut souvent l'inventaire réel et les fichiers
// vault — précisément ce qu'il faut pour rejouer le run. » Un .auditignore
// présent doit donc SUBSTITUER ses règles à celles de git, pas s'y ajouter :
// sinon l'inventaire reste exclu et l'audit reste inutile.
func TestAuditIgnoreRemplaceGitignore(t *testing.T) {
	dir := depotPerimetre(t)
	ecrire(t, dir, AuditIgnoreFile, "secrets/\n") // les vaults dehors, l'inventaire dedans

	p, err := DetermineScope(context.Background(), dir, Options{})
	if err != nil {
		t.Fatalf("DetermineScope: %v", err)
	}

	fichiers := p.Files()
	if !contient(fichiers, "inventaire.ini") {
		t.Errorf("l'inventaire, ignoré par git mais pas par %s, doit entrer dans le périmètre: %v",
			AuditIgnoreFile, fichiers)
	}
	if contient(fichiers, "secrets/vault.yml") {
		t.Errorf("le vault, exclu par %s, ne doit pas entrer: %v", AuditIgnoreFile, fichiers)
	}
}

// TestAuditIgnoreNeFiltrePasLesFichiersSuivis épingle la décision qui évite une
// ambiguïté de lecture indétectable.
//
// Un fichier suivi retiré du périmètre et un fichier suivi supprimé par
// l'utilisateur produisent le MÊME arbre, donc la même ligne de suppression
// dans le diff du §6.5. L'auditeur n'aurait aucun moyen de distinguer « le
// projet a choisi de ne pas auditer ce fichier » de « ce fichier a disparu ».
// Restreindre .auditignore aux fichiers non suivis supprime le cas par
// construction.
func TestAuditIgnoreNeFiltrePasLesFichiersSuivis(t *testing.T) {
	dir := depotPerimetre(t)
	ecrire(t, dir, AuditIgnoreFile, "site.yml\n") // site.yml est SUIVI

	p, err := DetermineScope(context.Background(), dir, Options{})
	if err != nil {
		t.Fatalf("DetermineScope: %v", err)
	}

	if !contient(p.Files(), "site.yml") {
		t.Errorf("un fichier suivi doit rester dans le périmètre malgré %s: %v",
			AuditIgnoreFile, p.Files())
	}
}

// TestInclusionsExplicites : la « liste d'inclusions explicites » du §5.3
// rattrape un fichier que les règles d'exclusion écartent.
func TestInclusionsExplicites(t *testing.T) {
	dir := depotPerimetre(t)

	p, err := DetermineScope(context.Background(), dir, Options{Include: []string{"inventaire.ini"}})
	if err != nil {
		t.Fatalf("DetermineScope: %v", err)
	}

	if !contient(p.Files(), "inventaire.ini") {
		t.Errorf("inclusion explicite ignorée: %v", p.Files())
	}
	if contient(p.Files(), "secrets/vault.yml") {
		t.Errorf("une inclusion explicite ne doit pas tout rapatrier: %v", p.Files())
	}
}

// TestInclusionVideNAjouteRien : sans pathspec, l'inclusion explicite ne doit
// pas dégénérer en « tous les fichiers ignorés » — ce qui embarquerait les
// arbres de build et les dépendances vendorées.
func TestInclusionVideNAjouteRien(t *testing.T) {
	dir := depotPerimetre(t)

	p, err := DetermineScope(context.Background(), dir, Options{Include: nil})
	if err != nil {
		t.Fatalf("DetermineScope: %v", err)
	}
	if len(p.Included) != 0 {
		t.Errorf("inclusions non vides sans pathspec: %v", p.Included)
	}
}

// TestFilesTrieEtDedoublonne : l'arbre d'audit doit être reproductible, donc la
// liste qui le construit doit l'être aussi.
func TestFilesTrieEtDedoublonne(t *testing.T) {
	p := &Scope{
		Tracked:   []string{"b.yml", "a.yml"},
		Untracked: []string{"c.yml"},
		Included:  []string{"a.yml"}, // doublon volontaire
	}

	got := p.Files()
	attendu := []string{"a.yml", "b.yml", "c.yml"}
	if len(got) != len(attendu) {
		t.Fatalf("Files() = %v, attendu %v", got, attendu)
	}
	for i := range attendu {
		if got[i] != attendu[i] {
			t.Fatalf("Files() = %v, attendu %v", got, attendu)
		}
	}
}

// TestDetermineScopeRefuseUnNonDepot : l'ADR-0003 exige un dépôt git, et
// l'erreur doit le dire au lieu d'échouer plus loin sur un symptôme.
func TestDetermineScopeRefuseUnNonDepot(t *testing.T) {
	if _, err := DetermineScope(context.Background(), t.TempDir(), Options{}); err == nil {
		t.Error("périmètre calculé hors d'un dépôt git")
	}
}

// TestValidateNoLFSRefuseUnPointeur : un pointeur capturé à la place de son
// contenu produit un audit qui a l'air complet et ne l'est pas.
func TestValidateNoLFSRefuseUnPointeur(t *testing.T) {
	dir := depot(t)
	ecrire(t, dir, "normal.yml", "- hosts: all\n")
	ecrire(t, dir, "gros.bin",
		"version https://git-lfs.github.com/spec/v1\noid sha256:abc\nsize 123\n")

	if err := ValidateNoLFS([]string{"normal.yml"}, dir); err != nil {
		t.Errorf("fichier normal refusé: %v", err)
	}

	err := ValidateNoLFS([]string{"normal.yml", "gros.bin"}, dir)
	if err == nil {
		t.Fatal("pointeur git-lfs accepté")
	}
	if !strings.Contains(err.Error(), "gros.bin") {
		t.Errorf("l'erreur ne nomme pas le fichier fautif: %v", err)
	}
}

// TestValidateNoLFSToleFichierAbsent : un fichier listé puis supprimé entre le
// ls-files et l'inspection n'est pas une erreur — update-index le traitera.
func TestValidateNoLFSToleFichierAbsent(t *testing.T) {
	dir := depot(t)
	if err := ValidateNoLFS([]string{"jamais-existe.yml"}, dir); err != nil {
		t.Errorf("fichier absent traité comme une erreur: %v", err)
	}
}

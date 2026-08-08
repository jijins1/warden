package upload

import (
	"context"
	"strings"
	"testing"

	"github.com/jijins1/warden/internal/git"
)

// TestAuditRef construit la ref du §5.2 et refuse ce qui la déformerait.
func TestAuditRef(t *testing.T) {
	const ulid = "01JZQ0000000000000000P0001"

	ref, err := AuditRef("alice", ulid)
	if err != nil {
		t.Fatalf("AuditRef: %v", err)
	}
	if string(ref) != "refs/audit/alice/"+ulid {
		t.Errorf("ref %q", ref)
	}

	refus := map[string][2]string{
		// Une barre oblique dans l'identifiant imbriquerait silencieusement la
		// ref d'un niveau, à l'intérieur de l'espace d'un autre utilisateur.
		"utilisateur avec barre oblique": {"alice/../bob", ulid},
		"utilisateur vide":               {"", ulid},
		"utilisateur avec espace":        {"al ice", ulid},
		"run_ref trop court":             {"alice", "01JZQ"},
		// I, L, O et U sont hors de l'alphabet Crockford d'un ULID.
		"run_ref hors alphabet": {"alice", "01JZQ000000000000000OTHER1"},
		"run_ref vide":          {"alice", ""},
	}
	for nom, cas := range refus {
		t.Run(nom, func(t *testing.T) {
			if _, err := AuditRef(cas[0], cas[1]); err == nil {
				t.Errorf("AuditRef(%q, %q) accepté", cas[0], cas[1])
			}
		})
	}
}

// TestMeasurePushCost mesure ce qu'un push devrait transporter quand la base
// est en avance sur ce que le serveur connaît (ADR-0003).
func TestMeasurePushCost(t *testing.T) {
	dir := depot(t)
	ctx := context.Background()

	ecrire(t, dir, "site.yml", "- hosts: all\n")
	lancer(t, dir, "add", "-A")
	lancer(t, dir, "commit", "-m", "connu du serveur")
	connu, _ := git.NewSHA(tete(t, dir))

	// Deux commits locaux non poussés : exactement le cas que l'ADR-0003 dit
	// non borné et demande de mesurer avant d'envoyer.
	for _, m := range []string{"local un", "local deux"} {
		ecrire(t, dir, "site.yml", "- hosts: all # "+m+"\n")
		lancer(t, dir, "add", "-A")
		lancer(t, dir, "commit", "-m", m)
	}
	base, _ := git.NewSHA(tete(t, dir))

	cout, err := MeasurePushCost(ctx, dir, connu, base)
	if err != nil {
		t.Fatalf("MeasurePushCost: %v", err)
	}
	if cout.MissingCommits != 2 {
		t.Errorf("MissingCommits = %d, attendu 2", cout.MissingCommits)
	}
	// Deux commits, deux arbres, deux blobs : la borne basse est le nombre de
	// commits, et fixer la valeur exacte figerait un détail de git.
	if cout.MissingObjects < cout.MissingCommits {
		t.Errorf("MissingObjects = %d, incohérent avec %d commits",
			cout.MissingObjects, cout.MissingCommits)
	}
	if cout.MissingBytes <= 0 {
		t.Errorf("MissingBytes = %d, des objets manquants ont forcément une taille", cout.MissingBytes)
	}
	if cout.Exceeds(WarningThreshold) {
		t.Errorf("trois commits minuscules dépassent le seuil de %d octets", int64(WarningThreshold))
	}
}

// TestMeasurePushCostRienAEnvoyer : quand le serveur connaît déjà la base, le
// push ne transporte rien et c'est le cas courant (ADR-0003).
func TestMeasurePushCostRienAEnvoyer(t *testing.T) {
	dir := depot(t)

	ecrire(t, dir, "site.yml", "- hosts: all\n")
	lancer(t, dir, "add", "-A")
	lancer(t, dir, "commit", "-m", "base")
	sha, _ := git.NewSHA(tete(t, dir))

	cout, err := MeasurePushCost(context.Background(), dir, sha, sha)
	if err != nil {
		t.Fatalf("MeasurePushCost: %v", err)
	}
	if cout.MissingCommits != 0 || cout.MissingObjects != 0 || cout.MissingBytes != 0 {
		t.Errorf("coût non nul pour une base déjà connue: %+v", cout)
	}
}

// TestMeasurePushCostBaseInconnueOriente : c'est le clone shallow de
// l'ADR-0003, « erreur, après tentative », et le message doit porter la
// commande qui débloque — sans quoi l'ingénieur repart vers le bastion SSH.
func TestMeasurePushCostBaseInconnueOriente(t *testing.T) {
	dir := depot(t)

	ecrire(t, dir, "site.yml", "- hosts: all\n")
	lancer(t, dir, "add", "-A")
	lancer(t, dir, "commit", "-m", "base")
	base, _ := git.NewSHA(tete(t, dir))

	absent := git.SHA("0123456789abcdef0123456789abcdef01234567")
	_, err := MeasurePushCost(context.Background(), dir, absent, base)
	if err == nil {
		t.Fatal("commit inconnu du clone accepté")
	}
	if !strings.Contains(err.Error(), "--unshallow") {
		t.Errorf("message non actionnable: %v", err)
	}
}

// TestPushRefuseUneRefInvalide : l'audit ne part pas vers une ref que la CLI
// n'a pas construite elle-même.
func TestPushRefuseUneRefInvalide(t *testing.T) {
	dir, base := depotSale(t)
	ctx := context.Background()

	perimetre, err := DetermineScope(ctx, dir, Options{})
	if err != nil {
		t.Fatalf("DetermineScope: %v", err)
	}
	audit, err := CaptureAudit(ctx, dir, base, perimetre, "audit", DefaultSignature(quand))
	if err != nil {
		t.Fatalf("CaptureAudit: %v", err)
	}
	defer func() { _ = audit.Close() }()

	if err := audit.Push(ctx, "https://warden.invalid", "jeton", "refs/audit/../heads/principale"); err == nil {
		t.Error("ref malformée acceptée")
	}
}

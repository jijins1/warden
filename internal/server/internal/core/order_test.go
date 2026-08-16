package core_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jijins1/warden/internal/server/internal/core"
)

type runsFactices map[int64]core.Run

func (r runsFactices) RunByID(_ context.Context, id int64) (core.Run, error) {
	run, ok := r[id]
	if !ok {
		return core.Run{}, errors.New("absent")
	}
	return run, nil
}

// frappeurFactice retient ce qu'on lui a demandé de signer — c'est cette partie
// qui porte les tests.
type frappeurFactice struct {
	depots     []string
	namespaces []string
	expirs     []time.Time
	err        error
}

func (f *frappeurFactice) CreateRunNamespace(context.Context, string, string, string) error {
	return nil
}

func (f *frappeurFactice) IssueFetchGrant(_ context.Context, repo, ns string, exp time.Time) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.depots = append(f.depots, repo)
	f.namespaces = append(f.namespaces, ns)
	f.expirs = append(f.expirs, exp)
	return "jeton-de-lecture", nil
}

func runValide() core.Run {
	return core.Run{
		ID: 1, RunRef: "01JZQ000000000000000000ABC", Repo: "depot",
		Environment: "prod", Inventory: "inv", Playbook: "site.yml",
		AuditSHA: "1111111111111111111111111111111111111111",
	}
}

// TestLOrdrePorteUnJetonScopeAuRun est la propriété centrale.
//
// Le jeton remis au pod n'ouvre QUE l'espace de noms de son run. C'est la
// « capacité réductible à un objet » de l'ADR-0007 : ce que le runner peut
// obtenir en abusant de son accès est exactement ce qu'il allait recevoir.
func TestLOrdrePorteUnJetonScopeAuRun(t *testing.T) {
	f := &frappeurFactice{}
	b, err := core.NewOrderBuilder(runsFactices{1: runValide()}, f, "https://git.invalid")
	if err != nil {
		t.Fatalf("NewOrderBuilder : %v", err)
	}

	ordre, err := b.OrderFor(t.Context(), 1)
	if err != nil {
		t.Fatalf("OrderFor : %v", err)
	}

	if len(f.namespaces) != 1 {
		t.Fatalf("%d jeton(s) frappé(s)", len(f.namespaces))
	}
	attendu := core.RunNamespaceFor(runValide().RunRef)
	if f.namespaces[0] != attendu {
		t.Errorf("espace de noms %q, attendu %q", f.namespaces[0], attendu)
	}
	if f.depots[0] != "depot" {
		t.Errorf("dépôt %q", f.depots[0])
	}
	if string(ordre.GitToken) != "jeton-de-lecture" {
		t.Errorf("jeton absent de l'ordre : %q", ordre.GitToken)
	}
	// L'origine porte le dépôt DU RUN : un dépôt bare par dépôt (ADR-0006).
	if ordre.GitURL != "https://git.invalid/depot.git" {
		t.Errorf("origine %q, attendue https://git.invalid/depot.git", ordre.GitURL)
	}
	if ordre.AuditSHA != runValide().AuditSHA {
		t.Errorf("commit à exécuter %q", ordre.AuditSHA)
	}
}

// TestLeJetonEstFrappeALaLivraison : sa vie commence quand le pod le reçoit.
//
// L'ADR-0018 fait construire l'ordre à la livraison et non au claim. Ce n'est pas
// un détail d'ordonnancement : un jeton frappé au claim aurait déjà consommé une
// partie de sa validité quand le pod l'obtient, et la durée devrait couvrir une
// attente qu'on ne sait pas borner.
func TestLeJetonEstFrappeALaLivraison(t *testing.T) {
	f := &frappeurFactice{}
	b, _ := core.NewOrderBuilder(runsFactices{1: runValide()}, f, "https://git.invalid")

	avant := time.Now()
	if _, err := b.OrderFor(t.Context(), 1); err != nil {
		t.Fatalf("OrderFor : %v", err)
	}

	reste := time.Until(f.expirs[0])
	if reste > core.FetchGrantTTL || reste < core.FetchGrantTTL-time.Since(avant)-time.Minute {
		t.Errorf("expiration à %v du TTL attendu %v", reste, core.FetchGrantTTL)
	}
}

// TestUnRunSansCommitNeProduitPasDOrdre.
//
// Un runner qui recevrait un commit vide clonerait, ne trouverait rien, et
// signalerait une panne de dépôt — alors que la cause serait un run déclaré sans
// push. Refuser ici nomme la vraie cause.
func TestUnRunSansCommitNeProduitPasDOrdre(t *testing.T) {
	sansCommit := runValide()
	sansCommit.AuditSHA = ""
	sansDepot := runValide()
	sansDepot.Repo = ""

	for nom, run := range map[string]core.Run{
		"sans commit d'audit": sansCommit,
		"sans dépôt":          sansDepot,
	} {
		t.Run(nom, func(t *testing.T) {
			f := &frappeurFactice{}
			b, _ := core.NewOrderBuilder(runsFactices{1: run}, f, "https://git.invalid")

			if _, err := b.OrderFor(t.Context(), 1); err == nil {
				t.Fatal("un ordre a été produit")
			}
			if len(f.namespaces) != 0 {
				t.Error("un jeton a été frappé pour un run inexécutable")
			}
		})
	}
}

// TestLOrdreNePorteNiCleNiVariables : ne pas donner l'illusion d'un contrôle qui
// n'existe pas.
//
// `key-inventory` n'a pas d'implémentation, et l'ADR-0020 a constaté que
// l'environnement cible n'a pas de gestionnaire de secrets : en v1 l'autorisation
// d'accès aux hôtes repose ENTIÈREMENT sur la NetworkPolicy egress. Une valeur
// factice ici laisserait croire le contraire.
func TestLOrdreNePorteNiCleNiVariables(t *testing.T) {
	b, _ := core.NewOrderBuilder(runsFactices{1: runValide()}, &frappeurFactice{}, "https://git.invalid")

	ordre, err := b.OrderFor(t.Context(), 1)
	if err != nil {
		t.Fatalf("OrderFor : %v", err)
	}
	if len(ordre.SSHKey) != 0 {
		t.Error("une clé d'accès est présente alors que key-inventory n'existe pas")
	}
	if len(ordre.ForwardEnv) != 0 {
		t.Error("des variables sont présentes alors que la CLI n'en envoie pas")
	}
}

// TestLOrigineDependDuDepotDuRun ferme un défaut qui n'aurait produit aucune
// erreur.
//
// Une origine constante ferait cloner le même dépôt à tous les runs. Le premier
// symptôme serait un runner exécutant le code d'un AUTRE projet — et rien en aval
// ne le rattraperait, puisque l'arbre obtenu serait un arbre valide.
func TestLOrigineDependDuDepotDuRun(t *testing.T) {
	autre := runValide()
	autre.ID = 2
	autre.Repo = "un-autre-depot"

	f := &frappeurFactice{}
	b, _ := core.NewOrderBuilder(runsFactices{1: runValide(), 2: autre}, f, "https://git.invalid/")

	premier, err := b.OrderFor(t.Context(), 1)
	if err != nil {
		t.Fatalf("OrderFor : %v", err)
	}
	second, err := b.OrderFor(t.Context(), 2)
	if err != nil {
		t.Fatalf("OrderFor : %v", err)
	}

	if premier.GitURL == second.GitURL {
		t.Fatalf("deux dépôts, la même origine : %q", premier.GitURL)
	}
	if second.GitURL != "https://git.invalid/un-autre-depot.git" {
		t.Errorf("origine %q", second.GitURL)
	}
}

// TestUneAdresseGitAbsenteEchoueAuDemarrage : échouer à froid plutôt qu'à la
// première livraison.
func TestUneAdresseGitAbsenteEchoueAuDemarrage(t *testing.T) {
	if _, err := core.NewOrderBuilder(runsFactices{}, &frappeurFactice{}, ""); err == nil {
		t.Fatal("un composeur sans adresse git a été construit")
	}
}

// TestUneFrappeEnEchecNeLivrePasDOrdreMuet : sans jeton, le runner ne peut rien
// tirer. Livrer l'ordre quand même produirait un échec côté pod, loin de la
// cause.
func TestUneFrappeEnEchecNeLivrePasDOrdreMuet(t *testing.T) {
	f := &frappeurFactice{err: errors.New("secret indisponible")}
	b, _ := core.NewOrderBuilder(runsFactices{1: runValide()}, f, "https://git.invalid")

	_, err := b.OrderFor(t.Context(), 1)
	if err == nil {
		t.Fatal("un ordre sans jeton a été livré")
	}
	if !strings.Contains(err.Error(), runValide().RunRef) {
		t.Errorf("le message ne nomme pas le run : %v", err)
	}
}

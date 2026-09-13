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
	b, err := core.NewOrderBuilder(runsFactices{1: runValide()}, f, nil, "https://git.invalid")
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
	b, _ := core.NewOrderBuilder(runsFactices{1: runValide()}, f, nil, "https://git.invalid")

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
			b, _ := core.NewOrderBuilder(runsFactices{1: run}, f, nil, "https://git.invalid")

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
	b, _ := core.NewOrderBuilder(runsFactices{1: runValide()}, &frappeurFactice{}, nil, "https://git.invalid")

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
	b, _ := core.NewOrderBuilder(runsFactices{1: runValide(), 2: autre}, f, nil, "https://git.invalid/")

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
	if _, err := core.NewOrderBuilder(runsFactices{}, &frappeurFactice{}, nil, ""); err == nil {
		t.Fatal("un composeur sans adresse git a été construit")
	}
}

// TestUneFrappeEnEchecNeLivrePasDOrdreMuet : sans jeton, le runner ne peut rien
// tirer. Livrer l'ordre quand même produirait un échec côté pod, loin de la
// cause.
func TestUneFrappeEnEchecNeLivrePasDOrdreMuet(t *testing.T) {
	f := &frappeurFactice{err: errors.New("secret indisponible")}
	b, _ := core.NewOrderBuilder(runsFactices{1: runValide()}, f, nil, "https://git.invalid")

	_, err := b.OrderFor(t.Context(), 1)
	if err == nil {
		t.Fatal("un ordre sans jeton a été livré")
	}
	if !strings.Contains(err.Error(), runValide().RunRef) {
		t.Errorf("le message ne nomme pas le run : %v", err)
	}
}

// --- la clé d'accès (ADR-0020) ---

// emetteurFactice retient ce qu'on lui a demandé d'émettre.
type emetteurFactice struct {
	users []string
	envs  []string
	cle   string
	err   error
}

func (e *emetteurFactice) Issue(_ context.Context, user, env string, _ []string) (core.AccessKey, error) {
	e.users = append(e.users, user)
	e.envs = append(e.envs, env)
	if e.err != nil {
		return core.AccessKey{}, e.err
	}
	return core.NewAccessKey([]byte(e.cle), time.Time{}), nil
}

func TestLOrdrePorteLaCleDeSonEnvironnement(t *testing.T) {
	e := &emetteurFactice{cle: "-----BEGIN OPENSSH PRIVATE KEY-----"}
	b, _ := core.NewOrderBuilder(runsFactices{1: runValide()}, &frappeurFactice{}, e, "https://git.invalid")
	b = b.AvecDroits(nouveauxDroits(cle(runValide().UserID, "prod")))

	ordre, err := b.OrderFor(t.Context(), 1)
	if err != nil {
		t.Fatalf("OrderFor : %v", err)
	}
	if string(ordre.SSHKey.Reveal()) != e.cle {
		t.Errorf("clé de l'ordre = %q", ordre.SSHKey.Reveal())
	}
	// La clé demandée est celle de l'ENVIRONNEMENT du run, pas d'un autre.
	if len(e.envs) != 1 || e.envs[0] != "prod" {
		t.Errorf("environnements demandés = %v, attendu [prod]", e.envs)
	}
	// `user` est passé même si la v1 l'ignore : c'est lui qui rendra
	// l'adaptateur Vault indolore (ADR-0020).
	if len(e.users) != 1 || e.users[0] == "" {
		t.Errorf("utilisateurs demandés = %v, il doit être transmis", e.users)
	}
}

// Un environnement sans clé n'est PAS une erreur : un playbook en
// `connection: local` n'en a besoin d'aucune, et c'est le cas de tous les runs
// du produit jusqu'ici.
func TestUnEnvironnementSansCleNEmpechePasLeRun(t *testing.T) {
	e := &emetteurFactice{err: core.ErrNoKey}
	b, _ := core.NewOrderBuilder(runsFactices{1: runValide()}, &frappeurFactice{}, e, "https://git.invalid")
	b = b.AvecDroits(nouveauxDroits(cle(runValide().UserID, "prod")))

	ordre, err := b.OrderFor(t.Context(), 1)
	if err != nil {
		t.Fatalf("OrderFor a échoué faute de clé : %v", err)
	}
	if len(ordre.SSHKey) != 0 {
		t.Errorf("clé = %q, attendu vide", ordre.SSHKey.Reveal())
	}
}

// Une PANNE du magasin de clés, en revanche, doit faire échouer la livraison :
// livrer un ordre sans clé parce que Kubernetes n'a pas répondu produirait un
// `unreachable` qui accuserait la machine cible.
func TestUnePanneDuMagasinDeClesFaitEchouerLaLivraison(t *testing.T) {
	e := &emetteurFactice{err: errors.New("API Kubernetes indisponible")}
	b, _ := core.NewOrderBuilder(runsFactices{1: runValide()}, &frappeurFactice{}, e, "https://git.invalid")
	b = b.AvecDroits(nouveauxDroits(cle(runValide().UserID, "prod")))

	if _, err := b.OrderFor(t.Context(), 1); err == nil {
		t.Fatal("la livraison a réussi malgré un magasin de clés en panne")
	}
}

// Sans magasin du tout — un déploiement sans accès au cluster — l'ordre part
// sans clé plutôt que d'échouer.
func TestSansMagasinDeClesLOrdrePartQuandMeme(t *testing.T) {
	b, _ := core.NewOrderBuilder(runsFactices{1: runValide()}, &frappeurFactice{}, nil, "https://git.invalid")

	ordre, err := b.OrderFor(t.Context(), 1)
	if err != nil {
		t.Fatalf("OrderFor : %v", err)
	}
	if len(ordre.SSHKey) != 0 {
		t.Errorf("clé = %q, attendu vide", ordre.SSHKey.Reveal())
	}
}

// --- l'autorisation à la livraison (§10.2, §10.3) ---

// LE test qui donne un sens à la révocation. `POST /runs` a déjà contrôlé le
// droit à la création ; si la clé n'était pas revérifiée ICI, retirer un droit ne
// ferait rien aux runs déjà enregistrés — et le §10.3 exige que le changement
// prenne effet.
func TestUnDroitRevoqueEmpecheLaLivraisonDeLaCle(t *testing.T) {
	e := &emetteurFactice{cle: "-----BEGIN OPENSSH PRIVATE KEY-----"}
	droits := nouveauxDroits(cle(runValide().UserID, "prod"))
	b, _ := core.NewOrderBuilder(runsFactices{1: runValide()}, &frappeurFactice{}, e, "https://git.invalid")
	b = b.AvecDroits(droits)

	if _, err := b.OrderFor(t.Context(), 1); err != nil {
		t.Fatalf("livraison refusée alors que le droit est vivant : %v", err)
	}

	delete(droits.accordes, cle(runValide().UserID, "prod")) // révocation

	_, err := b.OrderFor(t.Context(), 1)
	if !errors.Is(err, core.ErrEnvironnementInterdit) {
		t.Fatalf("err = %v : la clé a été livrée malgré un droit révoqué", err)
	}
	// Et le magasin de clés n'a même pas été SOLLICITÉ la seconde fois : le
	// refus précède la sortie du secret.
	if len(e.envs) != 1 {
		t.Errorf("le magasin de clés a été appelé %d fois, attendu 1", len(e.envs))
	}
}

// Sans magasin de droits, aucune clé ne sort. C'est le défaut voulu : un
// composeur mal assemblé ne doit pas distribuer des credentials.
func TestSansMagasinDeDroitsAucuneCleNeSort(t *testing.T) {
	e := &emetteurFactice{cle: "-----BEGIN OPENSSH PRIVATE KEY-----"}
	b, _ := core.NewOrderBuilder(runsFactices{1: runValide()}, &frappeurFactice{}, e, "https://git.invalid")

	if _, err := b.OrderFor(t.Context(), 1); !errors.Is(err, core.ErrEnvironnementInterdit) {
		t.Errorf("err = %v : une clé a été livrée sans magasin de droits", err)
	}
	if len(e.envs) != 0 {
		t.Error("le magasin de clés a été sollicité sans contrôle de droit")
	}
}

// LE test du maillon qui manquait.
//
// `POST /runs` acceptait un `flags`, la colonne `run.flags` le gardait, le
// runner savait le poser sur l'argv — et `OrderFor` ne le recopiait PAS dans
// l'ordre. Un `--check` demandé aurait donc été enregistré comme tel puis
// exécuté pour de vrai, ce qui est la pire des deux pannes possibles : l'audit
// dit « simulation » et la machine a changé.
func TestLOrdrePorteLesFlagsDuRun(t *testing.T) {
	run := runValide()
	run.Flags = []string{"--check", "--diff", "--limit=web-01"}

	b, err := core.NewOrderBuilder(runsFactices{1: run}, &frappeurFactice{}, nil, "https://git.invalid")
	if err != nil {
		t.Fatalf("NewOrderBuilder : %v", err)
	}

	ordre, err := b.OrderFor(t.Context(), 1)
	if err != nil {
		t.Fatalf("OrderFor : %v", err)
	}
	if strings.Join(ordre.Flags, " ") != "--check --diff --limit=web-01" {
		t.Fatalf("l'ordre porte %v, voulu les trois flags du run", ordre.Flags)
	}
}

// Un run sans flag part sans flag : le champ est `omitempty`, et un tableau vide
// dans l'ordre ferait composer un `cmdline` vide plutôt que pas de `cmdline`.
func TestUnRunSansFlagDonneUnOrdreSansFlag(t *testing.T) {
	b, err := core.NewOrderBuilder(runsFactices{1: runValide()}, &frappeurFactice{}, nil, "https://git.invalid")
	if err != nil {
		t.Fatalf("NewOrderBuilder : %v", err)
	}
	ordre, err := b.OrderFor(t.Context(), 1)
	if err != nil {
		t.Fatalf("OrderFor : %v", err)
	}
	if len(ordre.Flags) != 0 {
		t.Fatalf("ordre.Flags = %v, voulu vide", ordre.Flags)
	}
}

// Un run ad-hoc porte sa cible à la place du playbook : c'est le seul champ
// qui change, tout le reste — jeton git, origine, commit — se compose
// exactement comme pour un playbook.
func TestUnRunAdhocPorteSaCibleEtPasDePlaybook(t *testing.T) {
	run := runValide()
	run.Kind = core.RunAdhoc
	run.Playbook = ""
	run.AdhocTarget = &core.AdhocTarget{Module: "shell", ModuleArgs: "uptime", HostPattern: "web"}

	b, err := core.NewOrderBuilder(runsFactices{1: run}, &frappeurFactice{}, nil, "https://git.invalid")
	if err != nil {
		t.Fatalf("NewOrderBuilder : %v", err)
	}

	ordre, err := b.OrderFor(t.Context(), 1)
	if err != nil {
		t.Fatalf("OrderFor : %v", err)
	}
	if ordre.Playbook != "" {
		t.Errorf("ordre.Playbook = %q, un ad-hoc n'en a pas", ordre.Playbook)
	}
	if ordre.Module != "shell" || ordre.ModuleArgs != "uptime" || ordre.HostPattern != "web" {
		t.Errorf("cible ad-hoc mal reprise : module=%q args=%q motif=%q",
			ordre.Module, ordre.ModuleArgs, ordre.HostPattern)
	}
	// Le reste de l'ordre — jeton, origine, commit — vient de la même
	// composition que pour un playbook : rien ne le distingue à ce niveau.
	if ordre.GitURL != "https://git.invalid/depot.git" {
		t.Errorf("origine %q", ordre.GitURL)
	}
}

// Un ad-hoc SANS cible ne saurait pas quoi exécuter : le composeur d'ordres
// refuse plutôt que de livrer un `module`/`host_pattern` vides.
func TestUnRunAdhocSansCibleNeProduitPasDOrdre(t *testing.T) {
	run := runValide()
	run.Kind = core.RunAdhoc
	run.Playbook = ""
	run.AdhocTarget = nil

	b, err := core.NewOrderBuilder(runsFactices{1: run}, &frappeurFactice{}, nil, "https://git.invalid")
	if err != nil {
		t.Fatalf("NewOrderBuilder : %v", err)
	}
	if _, err := b.OrderFor(t.Context(), 1); err == nil {
		t.Fatal("un ordre a été produit pour un ad-hoc sans cible")
	}
}

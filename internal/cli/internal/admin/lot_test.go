package admin_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jijins1/warden/internal/cli/internal/admin"
	"github.com/jijins1/warden/internal/cli/internal/ansible"
	"github.com/jijins1/warden/internal/cli/internal/client"
	"github.com/jijins1/warden/internal/cli/internal/prompt"
	"github.com/jijins1/warden/internal/cli/internal/stream"
	"github.com/jijins1/warden/internal/events"
	"github.com/jijins1/warden/internal/wire/ws"
)

// projet monte un dépôt jetable : un fichier de projet, et un inventaire.
func projet(t *testing.T, inventaire string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".ansibleproxy.yml"),
		[]byte("inventory: inventories/{{ env }}\ndefault_env: prod\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inv := filepath.Join(dir, "inventories", "prod")
	if err := os.MkdirAll(inv, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inv, "hosts.yml"), []byte(inventaire), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

const inventaireDeTrois = `
all:
  vars:
    ansible_user: ansible
  children:
    cluster:
      hosts:
        kodi:
          ansible_host: 10.0.0.1
        muzi:
          ansible_host: 10.0.0.2
    impression:
      hosts:
        mainsail:
          ansible_host: 10.0.0.3
`

// --- la comparaison ---

func TestComparerSepareCeQuiManqueDeCeQuiDiffere(t *testing.T) {
	inventaire := []ansible.Machine{
		{Nom: "kodi", Adresse: "10.0.0.1", Login: "ansible"},
		{Nom: "muzi", Adresse: "10.0.0.9", Login: "ansible"}, // adresse changée
		{Nom: "tiny", Adresse: "10.0.0.3", Login: "ansible"}, // absente du serveur
	}
	connues := []client.Host{
		{Name: "kodi", Address: "10.0.0.1", Login: "ansible", Port: 22},
		{Name: "muzi", Address: "10.0.0.2", Login: "root", Port: 2222},
	}

	manquantes, ecarts := admin.Comparer(inventaire, connues)

	if len(manquantes) != 1 || manquantes[0].Nom != "tiny" {
		t.Fatalf("manquantes = %+v", manquantes)
	}
	if len(ecarts) != 1 || ecarts[0].Machine.Nom != "muzi" {
		t.Fatalf("écarts = %+v", ecarts)
	}
	// Les trois divergences sont nommées, pas seulement comptées.
	quoi := strings.Join(ecarts[0].Quoi, " ; ")
	for _, attendu := range []string{"adresse", "compte", "port"} {
		if !strings.Contains(quoi, attendu) {
			t.Errorf("l'écart ne nomme pas %q : %s", attendu, quoi)
		}
	}
}

// Un port absent des deux côtés vaut le défaut SSH : les comparer bruts
// signalerait un écart entre « 22 » et « rien », qui n'en est pas un.
func TestComparerNeVoitPasDEcartSurUnPortImplicite(t *testing.T) {
	_, ecarts := admin.Comparer(
		[]ansible.Machine{{Nom: "kodi", Adresse: "10.0.0.1", Login: "ansible"}},
		[]client.Host{{Name: "kodi", Address: "10.0.0.1", Login: "ansible", Port: 22}},
	)
	if len(ecarts) != 0 {
		t.Errorf("écarts = %+v", ecarts)
	}
}

// Un champ que l'inventaire ne pose pas ne contredit rien.
func TestComparerIgnoreCeQueLInventaireNeDitPas(t *testing.T) {
	_, ecarts := admin.Comparer(
		[]ansible.Machine{{Nom: "kodi", Adresse: "10.0.0.1"}},
		[]client.Host{{Name: "kodi", Address: "10.0.0.1", Login: "ansible", Port: 22}},
	)
	if len(ecarts) != 0 {
		t.Errorf("un login absent de l'inventaire a été pris pour un écart : %+v", ecarts)
	}
}

// --- le plan ---

func TestLePlanNeRetientQueCeQuiManque(t *testing.T) {
	dir := projet(t, inventaireDeTrois)
	api := serveur(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]client.Host{
			{Name: "kodi", Address: "10.0.0.1", Login: "ansible", Port: 22},
		})
	})

	var out strings.Builder
	plan, err := admin.PreparerLot(t.Context(), api,
		admin.OptionsInventaire{RepoDir: dir}, &out)
	if err != nil {
		t.Fatalf("PreparerLot: %v", err)
	}
	if len(plan.Manquantes) != 2 {
		t.Fatalf("manquantes = %+v", plan.Manquantes)
	}
	if plan.Environnement != "prod" {
		t.Errorf("environnement = %q, attendu prod (default_env)", plan.Environnement)
	}
	// Le plan dit D'OÙ il tient sa liste : sans ça, une erreur de template
	// d'inventaire ressemble à une flotte vide.
	if !strings.Contains(out.String(), "inventories/prod") {
		t.Errorf("le chemin lu n'est pas affiché :\n%s", out.String())
	}
	if strings.Contains(out.String(), "kodi") {
		t.Errorf("une machine déjà enrôlée figure au plan :\n%s", out.String())
	}
}

// Une adresse déduite du nom est MARQUÉE au plan. C'est le seul moment où
// quelqu'un peut objecter qu'un nom ne résoudra pas depuis le cluster.
func TestLePlanMarqueLesAdressesDeduites(t *testing.T) {
	dir := projet(t, "cluster:\n  hosts:\n    tiny:\n      ansible_user: ansible\n")
	api := serveur(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]client.Host{})
	})

	var out strings.Builder
	if _, err := admin.PreparerLot(t.Context(), api,
		admin.OptionsInventaire{RepoDir: dir}, &out); err != nil {
		t.Fatalf("PreparerLot: %v", err)
	}
	if !strings.Contains(out.String(), "(nom)") {
		t.Errorf("la déduction n'est pas marquée :\n%s", out.String())
	}
	if !strings.Contains(out.String(), "DEPUIS LE CLUSTER") {
		t.Errorf("la conséquence n'est pas dite :\n%s", out.String())
	}
}

// Les écarts sont AFFICHÉS et les machines ne sont pas touchées.
func TestLePlanSignaleLesEcartsSansLesCorriger(t *testing.T) {
	dir := projet(t, inventaireDeTrois)
	api := serveur(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("écriture inattendue : %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode([]client.Host{
			{Name: "kodi", Address: "10.9.9.9", Login: "ansible", Port: 22},
			{Name: "muzi", Address: "10.0.0.2", Login: "ansible", Port: 22},
			{Name: "mainsail", Address: "10.0.0.3", Login: "ansible", Port: 22},
		})
	})

	var out strings.Builder
	plan, err := admin.PreparerLot(t.Context(), api,
		admin.OptionsInventaire{RepoDir: dir}, &out)
	if err != nil {
		t.Fatalf("PreparerLot: %v", err)
	}
	if len(plan.Manquantes) != 0 {
		t.Fatalf("manquantes = %+v", plan.Manquantes)
	}
	if !strings.Contains(out.String(), "PAS touchées") {
		t.Errorf("l'inaction n'est pas dite :\n%s", out.String())
	}
	if !strings.Contains(out.String(), "Rien à faire") {
		t.Errorf("le cas « tout est déjà enrôlé » n'est pas dit :\n%s", out.String())
	}
}

// Une faute de frappe dans --group rendrait « aucune machine », ce qui ressemble
// beaucoup trop à « tout est déjà fait ».
func TestUnGroupeInconnuEstUneErreurQuiListeLesGroupes(t *testing.T) {
	dir := projet(t, inventaireDeTrois)
	api := serveur(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]client.Host{})
	})

	var out strings.Builder
	_, err := admin.PreparerLot(t.Context(), api,
		admin.OptionsInventaire{RepoDir: dir, Groupe: "clustre"}, &out)
	if err == nil {
		t.Fatal("un groupe inconnu a été accepté")
	}
	if !strings.Contains(err.Error(), "cluster") || !strings.Contains(err.Error(), "impression") {
		t.Errorf("l'erreur ne liste pas les groupes connus : %v", err)
	}
}

func TestLeGroupeRestreintLeLot(t *testing.T) {
	dir := projet(t, inventaireDeTrois)
	api := serveur(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]client.Host{})
	})

	var out strings.Builder
	plan, err := admin.PreparerLot(t.Context(), api,
		admin.OptionsInventaire{RepoDir: dir, Groupe: "impression"}, &out)
	if err != nil {
		t.Fatalf("PreparerLot: %v", err)
	}
	if len(plan.Manquantes) != 1 || plan.Manquantes[0].Nom != "mainsail" {
		t.Fatalf("manquantes = %+v", plan.Manquantes)
	}
}

// --- la déclaration depuis l'inventaire ---

func TestDeclarerDepuisInventaireNEnvoieQueLesManquantes(t *testing.T) {
	dir := projet(t, inventaireDeTrois)

	var declare client.Inventory
	api := serveur(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			_ = json.NewDecoder(r.Body).Decode(&declare)
			_ = json.NewEncoder(w).Encode(declare.Hosts)
			return
		}
		_ = json.NewEncoder(w).Encode([]client.Host{
			{Name: "kodi", Address: "10.0.0.1", Login: "ansible", Port: 22},
		})
	})

	var out strings.Builder
	if err := admin.DeclarerDepuisInventaire(t.Context(), api,
		admin.OptionsInventaire{RepoDir: dir}, &out); err != nil {
		t.Fatalf("DeclarerDepuisInventaire: %v", err)
	}

	if len(declare.Hosts) != 2 {
		t.Fatalf("machines déclarées = %+v", declare.Hosts)
	}
	for _, h := range declare.Hosts {
		if h.Name == "kodi" {
			t.Error("une machine déjà enrôlée a été redéclarée")
		}
		// Un inventaire Ansible ne porte pas de clé d'hôte : ces machines ne
		// sont pas épinglées, et rien ne doit prétendre le contraire.
		if h.HostKey != "" {
			t.Errorf("une empreinte a été inventée : %+v", h)
		}
	}
	if !strings.Contains(out.String(), "warden n'a rien posé") {
		t.Errorf("le rappel manque :\n%s", out.String())
	}
}

func TestDryRunNEcritRien(t *testing.T) {
	dir := projet(t, inventaireDeTrois)
	api := serveur(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("écriture malgré --dry-run : %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode([]client.Host{})
	})

	var out strings.Builder
	if err := admin.DeclarerDepuisInventaire(t.Context(), api,
		admin.OptionsInventaire{RepoDir: dir, DryRun: true}, &out); err != nil {
		t.Fatalf("DeclarerDepuisInventaire: %v", err)
	}
	if !strings.Contains(out.String(), "mainsail") {
		t.Errorf("le plan n'a pas été montré :\n%s", out.String())
	}
}

// --- le lot d'enrôlement ---

// suiveurDeLot rejoue un déroulement par machine, et retient l'ordre.
type suiveurDeLot struct {
	mu      sync.Mutex
	suivis  []string
	echouer map[string]bool
	hotes   map[string]string // run_ref -> machine
}

func (s *suiveurDeLot) Suivre(_ context.Context, runRef string, voir func(ws.EventFrame)) (stream.Fin, error) {
	s.mu.Lock()
	machine := s.hotes[runRef]
	s.suivis = append(s.suivis, machine)
	echoue := s.echouer[machine]
	s.mu.Unlock()

	voir(etape(0, "connexion", events.StateOK))
	if echoue {
		return stream.Fin{Status: "failed"}, nil
	}
	voir(empreinte(1))
	return stream.Fin{Status: "succeeded"}, nil
}

func lotDeTrois(t *testing.T, echecs ...string) (string, *client.Client, *suiveurDeLot, *int) {
	t.Helper()
	dir := projet(t, inventaireDeTrois)

	suiveur := &suiveurDeLot{echouer: map[string]bool{}, hotes: map[string]string{}}
	for _, e := range echecs {
		suiveur.echouer[e] = true
	}

	saisies := 0
	api := serveur(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/admin/enrollments":
			var n client.NewEnrollment
			_ = json.NewDecoder(r.Body).Decode(&n)
			suiveur.mu.Lock()
			suiveur.hotes[n.RunRef] = n.Host
			suiveur.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(client.EnrollmentCreated{RunRef: n.RunRef})
		case r.Method == http.MethodPut:
			_ = json.NewEncoder(w).Encode([]client.Host{})
		default:
			_ = json.NewEncoder(w).Encode([]client.Host{})
		}
	})
	return dir, api, suiveur, &saisies
}

func optionsLot(t *testing.T, dir, mdp string) admin.OptionsLot {
	t.Helper()
	return admin.OptionsLot{
		OptionsInventaire:  admin.OptionsInventaire{RepoDir: dir},
		MotDePasse:         &prompt.Source{Quoi: "le mot de passe", Fichier: ecrireCle(t, mdp)},
		ConfirmerEmpreinte: func(admin.Empreinte) bool { return true },
	}
}

// Le lot traite les machines UNE PAR UNE, dans l'ordre du plan.
func TestLeLotEnroleChaqueMachineDansLOrdre(t *testing.T) {
	dir, api, suiveur, _ := lotDeTrois(t)

	var out strings.Builder
	if err := admin.EnrolerLot(t.Context(), api, suiveur, optionsLot(t, dir, "hunter2"), &out); err != nil {
		t.Fatalf("EnrolerLot: %v", err)
	}

	attendu := []string{"kodi", "mainsail", "muzi"} // trié par nom
	if fmt.Sprint(suiveur.suivis) != fmt.Sprint(attendu) {
		t.Errorf("ordre = %v, attendu %v", suiveur.suivis, attendu)
	}
	if !strings.Contains(out.String(), "3 enrôlée(s), 0 en échec") {
		t.Errorf("récapitulatif :\n%s", out.String())
	}
}

// Un échec au milieu N'ARRÊTE PAS le lot : sur une flotte, l'échec d'une machine
// ne dit rien des autres.
func TestUnEchecNArretePasLeLot(t *testing.T) {
	dir, api, suiveur, _ := lotDeTrois(t, "mainsail")

	var out strings.Builder
	err := admin.EnrolerLot(t.Context(), api, suiveur, optionsLot(t, dir, "hunter2"), &out)
	if err == nil {
		t.Fatal("un lot avec un échec a rendu une réussite")
	}
	if len(suiveur.suivis) != 3 {
		t.Errorf("machines traitées = %v, attendu les 3", suiveur.suivis)
	}
	if !strings.Contains(out.String(), "2 enrôlée(s), 1 en échec") {
		t.Errorf("récapitulatif :\n%s", out.String())
	}
	// Le tableau final nomme LAQUELLE a échoué : c'est ce qu'on cherche après
	// dix machines, et le relire dans le défilement est ce qu'on veut éviter.
	if !strings.Contains(out.String(), "ÉCHEC") || !strings.Contains(out.String(), "mainsail") {
		t.Errorf("l'échec n'est pas au récapitulatif :\n%s", out.String())
	}
}

func TestStopOnErrorArreteLeLot(t *testing.T) {
	dir, api, suiveur, _ := lotDeTrois(t, "kodi")

	o := optionsLot(t, dir, "hunter2")
	o.ArreterAuPremierEchec = true

	var out strings.Builder
	if err := admin.EnrolerLot(t.Context(), api, suiveur, o, &out); err == nil {
		t.Fatal("un lot arrêté a rendu une réussite")
	}
	if len(suiveur.suivis) != 1 {
		t.Errorf("machines traitées = %v, attendu la première seulement", suiveur.suivis)
	}
	if !strings.Contains(out.String(), "non traitée(s)") {
		t.Errorf("les machines ignorées ne sont pas dites :\n%s", out.String())
	}
}

// lotAvecMotDePasseJetable monte un lot dont le fichier de mot de passe est
// SUPPRIMÉ dès que la première machine a été soumise.
//
// La suppression a lieu dans le handler HTTP, donc strictement après la lecture
// initiale et strictement avant que la deuxième machine ait besoin du secret :
// le nombre de lectures devient observable, sans course.
func lotAvecMotDePasseJetable(t *testing.T) (admin.OptionsLot, *client.Client, *suiveurDeLot) {
	t.Helper()
	dir := projet(t, inventaireDeTrois)
	chemin := ecrireCle(t, "hunter2")

	suiveur := &suiveurDeLot{echouer: map[string]bool{}, hotes: map[string]string{}}
	var premiere sync.Once
	api := serveur(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/admin/enrollments":
			var n client.NewEnrollment
			_ = json.NewDecoder(r.Body).Decode(&n)
			if n.Password != "hunter2" {
				t.Errorf("mot de passe transmis = %q", n.Password)
			}
			suiveur.mu.Lock()
			suiveur.hotes[n.RunRef] = n.Host
			suiveur.mu.Unlock()
			premiere.Do(func() { _ = os.Remove(chemin) })
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(client.EnrollmentCreated{RunRef: n.RunRef})
		default:
			_ = json.NewEncoder(w).Encode([]client.Host{})
		}
	})

	o := admin.OptionsLot{
		OptionsInventaire:  admin.OptionsInventaire{RepoDir: dir},
		MotDePasse:         &prompt.Source{Quoi: "le mot de passe", Fichier: chemin},
		ConfirmerEmpreinte: func(admin.Empreinte) bool { return true },
	}
	return o, api, suiveur
}

// UN SEUL mot de passe pour tout le lot : c'est le point de la commande.
func TestLeLotNeLitLeMotDePasseQuUneFois(t *testing.T) {
	o, api, suiveur := lotAvecMotDePasseJetable(t)

	var out strings.Builder
	if err := admin.EnrolerLot(t.Context(), api, suiveur, o, &out); err != nil {
		t.Fatalf("EnrolerLot: %v", err)
	}
	if len(suiveur.suivis) != 3 {
		t.Errorf("machines traitées = %v, attendu les 3 sur une seule saisie", suiveur.suivis)
	}
}

// Le miroir : --password-per-host relit à chaque machine. Le même montage, la
// même suppression, et cette fois la deuxième machine doit échouer — c'est ce
// qui prouve que le drapeau fait bien ce qu'il dit.
func TestPasswordPerHostRelitAChaqueMachine(t *testing.T) {
	o, api, suiveur := lotAvecMotDePasseJetable(t)
	o.ParMachine = true

	var out strings.Builder
	if err := admin.EnrolerLot(t.Context(), api, suiveur, o, &out); err == nil {
		t.Fatal("le lot a réussi alors que le mot de passe n'était plus lisible")
	}
	if len(suiveur.suivis) != 1 {
		t.Errorf("machines jointes = %v, attendu la première seulement", suiveur.suivis)
	}
}

// Le plan est confirmé AVANT la première connexion, et un refus ne joint rien.
func TestUnRefusDuPlanNeJointAucuneMachine(t *testing.T) {
	dir, api, suiveur, _ := lotDeTrois(t)

	o := optionsLot(t, dir, "hunter2")
	o.Confirmer = func([]ansible.Machine) bool { return false }

	var out strings.Builder
	if err := admin.EnrolerLot(t.Context(), api, suiveur, o, &out); err == nil {
		t.Fatal("un refus a rendu une réussite")
	}
	if len(suiveur.suivis) != 0 {
		t.Errorf("des machines ont été jointes malgré le refus : %v", suiveur.suivis)
	}
}

// --dry-run montre le plan et ne joint rien.
func TestLeLotEnDryRunNeJointRien(t *testing.T) {
	dir, api, suiveur, _ := lotDeTrois(t)

	o := optionsLot(t, dir, "hunter2")
	o.DryRun = true
	o.Confirmer = func([]ansible.Machine) bool {
		t.Error("le plan a été soumis malgré --dry-run")
		return false
	}

	var out strings.Builder
	if err := admin.EnrolerLot(t.Context(), api, suiveur, o, &out); err != nil {
		t.Fatalf("EnrolerLot: %v", err)
	}
	if len(suiveur.suivis) != 0 {
		t.Errorf("des machines ont été jointes : %v", suiveur.suivis)
	}
	if !strings.Contains(out.String(), "rien n'a été fait") {
		t.Errorf("sortie :\n%s", out.String())
	}
}

// Une machine sans `ansible_user` arrête le lot AVANT la première connexion :
// le découvrir à la septième, après six saisies, serait la pire façon de
// l'apprendre.
func TestUneMachineSansCompteArreteLeLotAvantToutContact(t *testing.T) {
	dir := projet(t, `
cluster:
  hosts:
    kodi:
      ansible_host: 10.0.0.1
      ansible_user: ansible
    orpheline:
      ansible_host: 10.0.0.2
`)
	suiveur := &suiveurDeLot{echouer: map[string]bool{}, hotes: map[string]string{}}
	api := serveur(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/admin/enrollments" {
			t.Error("une connexion a été tentée")
		}
		_ = json.NewEncoder(w).Encode([]client.Host{})
	})

	var out strings.Builder
	err := admin.EnrolerLot(t.Context(), api, suiveur, optionsLot(t, dir, "hunter2"), &out)
	if err == nil {
		t.Fatal("un lot incomplet a été accepté")
	}
	if !strings.Contains(err.Error(), "orpheline") || !strings.Contains(err.Error(), "ansible_user") {
		t.Errorf("l'erreur ne dit pas quoi corriger : %v", err)
	}
}

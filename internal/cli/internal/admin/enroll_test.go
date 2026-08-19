package admin_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/jijins1/warden/internal/cli/internal/admin"
	"github.com/jijins1/warden/internal/cli/internal/client"
	"github.com/jijins1/warden/internal/cli/internal/prompt"
	"github.com/jijins1/warden/internal/cli/internal/stream"
	"github.com/jijins1/warden/internal/events"
	"github.com/jijins1/warden/internal/wire/ws"
)

const (
	empreinteLisible = "SHA256:K2HA77aKM3exemple"
	cleDHote         = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExemple"
)

// suiveurFactice rejoue un déroulement d'enrôlement.
type suiveurFactice struct {
	frames []ws.EventFrame
	fin    stream.Fin
	err    error
	vu     string // le run_ref suivi
}

func (s *suiveurFactice) Suivre(_ context.Context, runRef string, voir func(ws.EventFrame)) (stream.Fin, error) {
	s.vu = runRef
	if s.err != nil {
		return stream.Fin{}, s.err
	}
	for _, f := range s.frames {
		voir(f)
	}
	return s.fin, nil
}

func etape(seq int64, texte string, etat events.State) ws.EventFrame {
	charge, _ := json.Marshal(map[string]string{"warden_enrolement": texte})
	return ws.EventFrame{Seq: seq, State: etat, Payload: charge}
}

func empreinte(seq int64) ws.EventFrame {
	charge, _ := json.Marshal(map[string]any{
		"warden_host_key":      cleDHote,
		"warden_fingerprint":   empreinteLisible,
		"warden_deja_presente": false,
	})
	return ws.EventFrame{Seq: seq, State: events.StateChanged, Payload: charge}
}

func deroulementReussi() *suiveurFactice {
	return &suiveurFactice{
		frames: []ws.EventFrame{
			etape(0, "connexion à 10.20.0.42:22 en tant que ansible", events.StateOK),
			etape(1, "empreinte de la machine : "+empreinteLisible, events.StateOK),
			empreinte(2),
			etape(3, "clé ajoutée à authorized_keys", events.StateOK),
			etape(4, "enrôlement vérifié", events.StateOK),
		},
		fin: stream.Fin{Status: "succeeded"},
	}
}

func optionsEnrolement(t *testing.T, mdp string) admin.OptionsEnrolement {
	t.Helper()
	return admin.OptionsEnrolement{
		Environment: "prod", Host: "db-prod-02", Address: "10.20.0.42",
		Port: 22, Login: "ansible",
		MotDePasse: &prompt.Source{Quoi: "le mot de passe", Fichier: ecrireCle(t, mdp)},
		Confirmer:  func(admin.Empreinte) bool { return true },
	}
}

// --- le chemin nominal ---

func TestUnEnrolementDeclareLaMachineAvecSonEmpreinte(t *testing.T) {
	var cree client.NewEnrollment
	var declare client.Inventory

	api := serveur(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/admin/enrollments":
			_ = json.NewDecoder(r.Body).Decode(&cree)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(client.EnrollmentCreated{RunRef: cree.RunRef})
		case "/admin/hosts":
			_ = json.NewDecoder(r.Body).Decode(&declare)
			_ = json.NewEncoder(w).Encode([]client.Host{})
		default:
			t.Errorf("chemin inattendu : %s", r.URL.Path)
		}
	})

	suiveur := deroulementReussi()
	o := optionsEnrolement(t, "hunter2")

	var out strings.Builder
	if err := admin.Enroler(context.Background(), api, suiveur, o, &out); err != nil {
		t.Fatalf("Enroler: %v", err)
	}

	// Le mot de passe part vers le serveur, une fois.
	if cree.Password != "hunter2" || cree.Host != "db-prod-02" {
		t.Errorf("enrôlement déclaré = %+v", cree)
	}
	// Le run suivi est celui qui vient d'être créé.
	if suiveur.vu != cree.RunRef || cree.RunRef == "" {
		t.Errorf("run suivi = %q, créé = %q", suiveur.vu, cree.RunRef)
	}

	// LA propriété : la machine est déclarée avec l'empreinte CONSTATÉE. Sans
	// elle, on l'enregistrerait non épinglée en la croyant enrôlée.
	if len(declare.Hosts) != 1 {
		t.Fatalf("machines déclarées = %d, attendu 1", len(declare.Hosts))
	}
	if declare.Hosts[0].HostKey != cleDHote {
		t.Errorf("empreinte déclarée = %q, attendu %q", declare.Hosts[0].HostKey, cleDHote)
	}

	// Le récit est affiché : c'est de l'audit, pas une barre de progression.
	for _, attendu := range []string{"connexion", "authorized_keys", "vérifié", empreinteLisible} {
		if !strings.Contains(out.String(), attendu) {
			t.Errorf("la sortie ne mentionne pas %q :\n%s", attendu, out.String())
		}
	}
}

// --- la revue de l'empreinte ---

// LE moment qui compte. Sans empreinte déclarée à l'avance, l'enrôlement fait
// confiance à ce qui répond ; ce refus est le seul endroit où quelqu'un peut
// encore dire non.
func TestUnRefusDeLEmpreinteNeDeclarePasLaMachine(t *testing.T) {
	declaree := false
	api := serveur(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/admin/hosts" {
			declaree = true
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(client.EnrollmentCreated{RunRef: "x"})
	})

	o := optionsEnrolement(t, "hunter2")
	o.Confirmer = func(admin.Empreinte) bool { return false }

	var out strings.Builder
	err := admin.Enroler(context.Background(), api, deroulementReussi(), o, &out)
	if err == nil {
		t.Fatal("un refus a été traité comme un succès")
	}
	if declaree {
		t.Error("la machine a été déclarée malgré le refus")
	}
	// Et l'utilisateur doit savoir que la clé EST posée : abandonner la
	// déclaration ne défait rien sur la machine.
	if !strings.Contains(out.String(), "clé EST posée") {
		t.Errorf("la sortie ne dit pas que la clé reste posée :\n%s", out.String())
	}
}

// La confirmation au terminal refuse par DÉFAUT : accepter une empreinte par
// distraction est ce que cette question existe pour empêcher.
func TestLaConfirmationRefuseParDefaut(t *testing.T) {
	cas := map[string]bool{
		"o":         true,
		"oui":       true,
		"y":         true,
		"":          false,
		"\n":        false,
		"n":         false,
		"peut-être": false,
		"OUI":       true,
	}
	for reponse, attendu := range cas {
		t.Run(reponse, func(t *testing.T) {
			var out strings.Builder
			got := admin.ConfirmerAuTerminal(strings.NewReader(reponse+"\n"), &out)(
				admin.Empreinte{Valeur: empreinteLisible, Algorithme: "ssh-ed25519"})
			if got != attendu {
				t.Errorf("réponse %q → %v, attendu %v", reponse, got, attendu)
			}
			// La question doit dire COMMENT vérifier, sinon elle n'est qu'un
			// clic de plus.
			if !strings.Contains(out.String(), "ssh-keygen") {
				t.Errorf("la question ne dit pas comment comparer :\n%s", out.String())
			}
		})
	}
}

// --- les échecs ---

// Un run qui réussit sans rapporter d'empreinte ne doit PAS déclarer la
// machine : on l'enregistrerait non épinglée en la croyant enrôlée.
func TestUnSuccesSansEmpreinteNeDeclarePas(t *testing.T) {
	declaree := false
	api := serveur(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/admin/hosts" {
			declaree = true
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(client.EnrollmentCreated{RunRef: "x"})
	})

	suiveur := &suiveurFactice{
		frames: []ws.EventFrame{etape(0, "connexion", events.StateOK)},
		fin:    stream.Fin{Status: "succeeded"},
	}

	var out strings.Builder
	if err := admin.Enroler(context.Background(), api, suiveur, optionsEnrolement(t, "x"), &out); err == nil {
		t.Fatal("un succès sans empreinte a été accepté")
	}
	if declaree {
		t.Error("la machine a été déclarée sans empreinte")
	}
}

// Un enrôlement en échec affiche quand même l'empreinte quand il l'a vue : il a
// appris quelque chose, et c'est ce qu'il faut pour épingler ensuite.
func TestUnEchecAfficheLEmpreinteConstatee(t *testing.T) {
	api := serveur(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(client.EnrollmentCreated{RunRef: "x"})
	})

	suiveur := &suiveurFactice{
		frames: []ws.EventFrame{
			empreinte(0),
			etape(1, "la machine a refusé le login et le mot de passe", events.StateFailed),
		},
		fin: stream.Fin{Status: "failed"},
	}

	var out strings.Builder
	if err := admin.Enroler(context.Background(), api, suiveur, optionsEnrolement(t, "x"), &out); err == nil {
		t.Fatal("un échec a été traité comme un succès")
	}
	if !strings.Contains(out.String(), empreinteLisible) {
		t.Errorf("l'empreinte constatée n'est pas affichée :\n%s", out.String())
	}
}

func TestUneCibleIncompleteEstRefuseeAvantToutAppel(t *testing.T) {
	api := serveur(t, func(http.ResponseWriter, *http.Request) {
		t.Error("le serveur a été appelé alors que la commande est incomplète")
	})

	complet := optionsEnrolement(t, "x")
	cas := map[string]func(admin.OptionsEnrolement) admin.OptionsEnrolement{
		"sans environnement": func(o admin.OptionsEnrolement) admin.OptionsEnrolement { o.Environment = ""; return o },
		"sans machine":       func(o admin.OptionsEnrolement) admin.OptionsEnrolement { o.Host = ""; return o },
		"sans adresse":       func(o admin.OptionsEnrolement) admin.OptionsEnrolement { o.Address = ""; return o },
		"sans compte":        func(o admin.OptionsEnrolement) admin.OptionsEnrolement { o.Login = ""; return o },
		"sans mot de passe":  func(o admin.OptionsEnrolement) admin.OptionsEnrolement { o.MotDePasse = nil; return o },
	}
	for nom, casser := range cas {
		t.Run(nom, func(t *testing.T) {
			var out strings.Builder
			if err := admin.Enroler(context.Background(), api, deroulementReussi(), casser(complet), &out); err == nil {
				t.Fatal("accepté à tort")
			}
		})
	}
}

// LE défaut trouvé sur une vraie machine. Un sshd sert plusieurs clés d'hôte et
// c'est la NÉGOCIATION qui décide laquelle : la question doit nommer le fichier
// de l'algorithme réellement retenu, sinon l'utilisateur constate un écart
// légitime — et apprend à passer outre sur une question de sécurité.
func TestLaQuestionNommeLeFichierDeLAlgorithmeNegocie(t *testing.T) {
	cas := map[string]string{
		"ssh-ed25519":         "/etc/ssh/ssh_host_ed25519_key.pub",
		"ecdsa-sha2-nistp256": "/etc/ssh/ssh_host_ecdsa_key.pub",
		"ecdsa-sha2-nistp521": "/etc/ssh/ssh_host_ecdsa_key.pub",
		"ssh-rsa":             "/etc/ssh/ssh_host_rsa_key.pub",
		"rsa-sha2-512":        "/etc/ssh/ssh_host_rsa_key.pub",
	}
	for algo, fichier := range cas {
		t.Run(algo, func(t *testing.T) {
			var out strings.Builder
			admin.ConfirmerAuTerminal(strings.NewReader("n\n"), &out)(
				admin.Empreinte{Valeur: empreinteLisible, Algorithme: algo})
			if !strings.Contains(out.String(), fichier) {
				t.Errorf("la question ne nomme pas %s :\n%s", fichier, out.String())
			}
		})
	}

	// Un algorithme inconnu ne doit désigner AUCUN fichier au hasard : mieux
	// vaut inviter à les lister tous que faire comparer la mauvaise clé.
	var out strings.Builder
	admin.ConfirmerAuTerminal(strings.NewReader("n\n"), &out)(
		admin.Empreinte{Valeur: empreinteLisible, Algorithme: "algo-du-futur"})
	if !strings.Contains(out.String(), "ssh_host_*_key.pub") {
		t.Errorf("un algorithme inconnu désigne un fichier précis :\n%s", out.String())
	}
}

// L'algorithme est celui de la ligne rapportée par le pod, pas une supposition.
func TestLAlgorithmeAfficheVientDeLaCleConstatee(t *testing.T) {
	api := serveur(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(client.EnrollmentCreated{RunRef: "x"})
	})

	var vue admin.Empreinte
	o := optionsEnrolement(t, "hunter2")
	o.Confirmer = func(e admin.Empreinte) bool { vue = e; return false }

	var out strings.Builder
	_ = admin.Enroler(context.Background(), api, deroulementReussi(), o, &out)

	if vue.Algorithme != "ssh-ed25519" {
		t.Errorf("algorithme = %q, attendu celui de la clé constatée", vue.Algorithme)
	}
	if vue.Valeur != empreinteLisible {
		t.Errorf("empreinte = %q", vue.Valeur)
	}
}

// Le `run_ref` accompagne la déclaration : sans lui le serveur ne peut pas
// CONSTATER que warden s'est connecté, et la machine serait notée `declared` —
// ce qui dirait que le produit n'a jamais vu de mot de passe, alors qu'il en a
// vu un. Le mode n'est pas dans le corps, il est déduit côté serveur.
func TestLaDeclarationPorteLeRunDEnrolement(t *testing.T) {
	var cree client.NewEnrollment
	var requeteHosts string

	api := serveur(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/admin/enrollments":
			_ = json.NewDecoder(r.Body).Decode(&cree)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(client.EnrollmentCreated{RunRef: cree.RunRef})
		case "/admin/hosts":
			requeteHosts = r.URL.RawQuery
			_ = json.NewEncoder(w).Encode([]client.Host{})
		}
	})

	var out strings.Builder
	if err := admin.Enroler(context.Background(), api, deroulementReussi(),
		optionsEnrolement(t, "hunter2"), &out); err != nil {
		t.Fatalf("Enroler: %v", err)
	}

	if !strings.Contains(requeteHosts, "enrollment="+cree.RunRef) {
		t.Errorf("la déclaration ne porte pas le run d'enrôlement : %q", requeteHosts)
	}
	if !strings.Contains(requeteHosts, "environment=prod") {
		t.Errorf("la déclaration ne porte pas l'environnement : %q", requeteHosts)
	}
}

// LE cas de l'intermédiaire, vu depuis la CLI. Le runner le distingue par
// l'état `unreachable` ; la CLI doit dire d'ENQUÊTER, pas de réessayer — un
// message générique produit exactement la mauvaise conduite.
func TestUneMachineSubstitueeEstNommeeCommeTelle(t *testing.T) {
	declaree := false
	api := serveur(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/admin/hosts" {
			declaree = true
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(client.EnrollmentCreated{RunRef: "x"})
	})

	suiveur := &suiveurFactice{
		frames: []ws.EventFrame{
			etape(0, "connexion à 10.20.0.42:22 en tant que ansible", events.StateOK),
			etape(1, "l'empreinte de la machine ne correspond pas à celle attendue",
				events.StateUnreachable),
		},
		fin: stream.Fin{Status: "failed"},
	}

	var out strings.Builder
	err := admin.Enroler(context.Background(), api, suiveur, optionsEnrolement(t, "x"), &out)
	if err == nil {
		t.Fatal("une machine substituée a été acceptée")
	}
	if declaree {
		t.Error("la machine substituée a été déclarée")
	}

	// Le message doit décourager la reprise et nommer les deux causes
	// possibles : sans cela, l'utilisateur relance la commande.
	for _, attendu := range []string{"N'insistez pas", "réinstallée", "répond à sa place"} {
		if !strings.Contains(out.String(), attendu) {
			t.Errorf("la sortie ne dit pas %q :\n%s", attendu, out.String())
		}
	}
}

// Un échec ORDINAIRE ne doit pas porter le message de substitution : crier au
// loup sur un mot de passe erroné apprendrait à ignorer le vrai avertissement.
func TestUnEchecOrdinaireNeCriePasALaSubstitution(t *testing.T) {
	api := serveur(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(client.EnrollmentCreated{RunRef: "x"})
	})

	suiveur := &suiveurFactice{
		frames: []ws.EventFrame{
			etape(0, "la machine a refusé le login et le mot de passe", events.StateFailed),
		},
		fin: stream.Fin{Status: "failed"},
	}

	var out strings.Builder
	if err := admin.Enroler(context.Background(), api, suiveur, optionsEnrolement(t, "x"), &out); err == nil {
		t.Fatal("un échec a été traité comme un succès")
	}
	if strings.Contains(out.String(), "N'insistez pas") {
		t.Errorf("un échec ordinaire porte le message de substitution :\n%s", out.String())
	}
}

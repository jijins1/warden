// Étage 1 de l'ADR-0010 : un httptest.Server qui parle SSE, aucun cluster.
//
// Ce paquet vit dans le domaine hostile et manipule un credential : c'est le
// pire ratio importance/couverture qu'avait le dépôt, et ce fichier le comble.

package dispatch_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jijins1/warden/internal/runner/internal/dispatch"
)

const jetonDeTest = "jeton-d-amorcage-du-pod"

// trame rend une trame SSE du protocole de dispatch.
func trame(kind, corps string) string {
	return "event: " + kind + "\ndata: " + corps + "\n\n"
}

// serveur monte un canal qui écrit ce qu'on lui donne, et retient l'autorisation
// qu'il a vue.
func serveur(t *testing.T, ecrire func(w http.ResponseWriter, f http.Flusher)) (string, *atomic.Pointer[string]) {
	t.Helper()
	vu := &atomic.Pointer[string]{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a := r.Header.Get("Authorization"); a != "" {
			vu.Store(&a)
		}
		f, ok := w.(http.Flusher)
		if !ok {
			t.Error("le serveur de test ne sait pas flusher")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f.Flush()
		ecrire(w, f)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, vu
}

func ordreJSON(runRef string) string {
	return fmt.Sprintf(`{"order":{"run_ref":%q,"audit_sha":"%s","git_url":"https://git.invalid",`+
		`"git_token":"amNvbg=="}}`, runRef, strings.Repeat("a", 40))
}

// TestLeJetonVoyageEnEnTete : c'est la seule chose qui identifie ce pod.
func TestLeJetonVoyageEnEnTete(t *testing.T) {
	url, vu := serveur(t, func(w http.ResponseWriter, f http.Flusher) {
		_, _ = w.Write([]byte(trame("order", ordreJSON("01JZQ000000000000000000ABC"))))
		f.Flush()
	})

	c, err := dispatch.New(url, jetonDeTest)
	if err != nil {
		t.Fatalf("New : %v", err)
	}
	if _, err := c.Await(t.Context()); err != nil {
		t.Fatalf("Await : %v", err)
	}

	entete := vu.Load()
	if entete == nil || *entete != "Bearer "+jetonDeTest {
		t.Errorf("autorisation transmise : %v", entete)
	}
}

// TestLOrdreEstRenduTelQuel : le runner exécute ce que l'ordre dit, donc l'ordre
// doit traverser sans être réinterprété.
func TestLOrdreEstRenduTelQuel(t *testing.T) {
	const ref = "01JZQ000000000000000000ABC"
	url, _ := serveur(t, func(w http.ResponseWriter, f http.Flusher) {
		_, _ = w.Write([]byte(trame("order", ordreJSON(ref))))
		f.Flush()
	})

	c, _ := dispatch.New(url, jetonDeTest)
	ordre, err := c.Await(t.Context())
	if err != nil {
		t.Fatalf("Await : %v", err)
	}
	if ordre.RunRef != ref {
		t.Errorf("run_ref %q, attendu %q", ordre.RunRef, ref)
	}
	if ordre.GitURL != "https://git.invalid" {
		t.Errorf("git_url %q", ordre.GitURL)
	}
	if len(ordre.GitToken) == 0 {
		t.Error("le jeton de lecture est absent de l'ordre")
	}
}

// TestLesTramesDeMaintienEtLesTypesInconnusSontIgnores.
//
// Le format est extensible EXPRÈS (ADR-0018) : ignorer ce qu'on ne connaît pas
// est ce qui permettra d'ajouter un message sans casser les runners déjà
// déployés — et un runner déployé, c'est un pod qu'on ne met pas à jour.
func TestLesTramesDeMaintienEtLesTypesInconnusSontIgnores(t *testing.T) {
	url, _ := serveur(t, func(w http.ResponseWriter, f http.Flusher) {
		// Commentaire SSE : c'est ainsi qu'on tient la connexion à travers un
		// proxy sans inventer de message.
		_, _ = w.Write([]byte(": keep-alive\n\n"))
		f.Flush()
		_, _ = w.Write([]byte(trame("hello", `{}`)))
		f.Flush()
		_, _ = w.Write([]byte(trame("un-type-du-futur", `{}`)))
		f.Flush()
		_, _ = w.Write([]byte(trame("order", ordreJSON("01JZQ000000000000000000ABC"))))
		f.Flush()
	})

	c, _ := dispatch.New(url, jetonDeTest)
	ordre, err := c.Await(t.Context())
	if err != nil {
		t.Fatalf("Await : %v", err)
	}
	if ordre.RunRef == "" {
		t.Error("l'ordre a été perdu au milieu des trames ignorées")
	}
}

// TestUneAnnulationAvantLOrdreEstDistinguee.
//
// Cas réel : un Ctrl-C pendant le démarrage du pod (§5.6). Ce n'est pas un échec
// de l'agent, et le confondre avec une panne ferait remonter une erreur là où il
// n'y a qu'un utilisateur qui a changé d'avis.
func TestUneAnnulationAvantLOrdreEstDistinguee(t *testing.T) {
	url, _ := serveur(t, func(w http.ResponseWriter, f http.Flusher) {
		_, _ = w.Write([]byte(trame("cancel", `{"reason":"user"}`)))
		f.Flush()
	})

	c, _ := dispatch.New(url, jetonDeTest)
	_, err := c.Await(t.Context())
	if !errors.Is(err, dispatch.ErrCanceled) {
		t.Fatalf("erreur non distinguée : %v", err)
	}
}

// TestLeCanalNeSeRouvrePas épingle une décision de l'ADR-0018.
//
// Si la connexion tombe, le pod est perdu pour le control plane, qui le
// remplacera. Réessayer ici donnerait l'illusion d'une résilience que le
// protocole n'a pas — et deux pods se croyant titulaires du même run seraient
// bien pires qu'un pod mort.
func TestLeCanalNeSeRouvrePas(t *testing.T) {
	var connexions atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connexions.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// Le flux se ferme sans avoir rien livré.
	}))
	t.Cleanup(srv.Close)

	c, _ := dispatch.New(srv.URL, jetonDeTest)
	if _, err := c.Await(t.Context()); err == nil {
		t.Fatal("un flux fermé sans ordre a été accepté")
	}
	if n := connexions.Load(); n != 1 {
		t.Errorf("%d connexions ouvertes, attendu 1 : le canal a été rouvert", n)
	}
}

// TestUnRefusNApprendRienDePlusQueLeServeurNenDit.
//
// Le serveur ne dit rien, délibérément : un porteur de jeton ne doit pas
// apprendre si un pod a existé ni s'il a été libéré. Le message d'ici ne peut
// donc pas être plus précis, et prétendre le contraire serait trompeur pour qui
// lit les logs du pod.
func TestUnRefusNApprendRienDePlusQueLeServeurNenDit(t *testing.T) {
	for _, statut := range []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusGone} {
		t.Run(http.StatusText(statut), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(statut)
			}))
			t.Cleanup(srv.Close)

			c, _ := dispatch.New(srv.URL, jetonDeTest)
			_, err := c.Await(t.Context())
			if err == nil {
				t.Fatal("un refus a été accepté")
			}
			// Le jeton ne doit jamais se retrouver dans un message d'erreur : les
			// logs d'un pod sont collectés, et un credential collecté est un
			// credential fuité.
			if strings.Contains(err.Error(), jetonDeTest) {
				t.Errorf("le jeton fuite dans l'erreur : %v", err)
			}
		})
	}
}

// TestUnContexteAnnuleRendLaMain : le pod doit pouvoir s'arrêter proprement à
// SIGTERM, y compris avant d'avoir reçu son ordre.
func TestUnContexteAnnuleRendLaMain(t *testing.T) {
	url, _ := serveur(t, func(w http.ResponseWriter, f http.Flusher) {
		// Le serveur ne dit jamais rien : seul le contexte peut débloquer.
		<-t.Context().Done()
	})

	ctx, annuler := context.WithCancel(t.Context())
	c, _ := dispatch.New(url, jetonDeTest)

	fini := make(chan error, 1)
	go func() {
		_, err := c.Await(ctx)
		fini <- err
	}()

	annuler()
	select {
	case err := <-fini:
		if err == nil {
			t.Fatal("Await a réussi malgré l'annulation")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Await n'a pas rendu la main après annulation du contexte")
	}
}

// TestUneConfigurationInutilisableEchoueAvantToutAppel : échouer avant d'ouvrir
// une connexion, plutôt qu'au premier octet.
func TestUneConfigurationInutilisableEchoueAvantToutAppel(t *testing.T) {
	cas := map[string][2]string{
		"adresse vide":           {"", jetonDeTest},
		"jeton vide":             {"http://serveur.invalid", ""},
		"transport non http":     {"ssh://serveur.invalid", jetonDeTest},
		"adresse sans protocole": {"serveur.invalid", jetonDeTest},
	}
	for nom, c := range cas {
		t.Run(nom, func(t *testing.T) {
			if _, err := dispatch.New(c[0], c[1]); err == nil {
				t.Fatal("configuration acceptée")
			}
		})
	}
}

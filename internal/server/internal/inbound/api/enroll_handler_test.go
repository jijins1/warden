// Étage 1 de l'ADR-0010 : ni base, ni cluster.
//
// La propriété testée est la REPRISE. La CLI réutilise délibérément le même
// `run_ref` pour reprendre un enrôlement interrompu — c'est écrit dans son aide
// et dans le commentaire de `enrollPayload` — et la première étape de la
// création est idempotente exprès pour cela. La seconde ne peut pas l'être : un
// run est un enregistrement d'audit, et le recréer sous le même identifiant
// effacerait le premier.
//
// Reste donc à répondre quelque chose d'UTILISABLE. Ce fichier fixe ce que
// « utilisable » veut dire, cas par cas.

package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jijins1/warden/internal/server/internal/core"
)

// createurSature refuse toute création : le `run_ref` existe déjà.
type createurSature struct{}

func (createurSature) CreateRun(context.Context, core.NewRun) (int64, error) {
	return 0, core.ErrRunRefExists
}

// consigneFactice retient ce qu'on lui range, et dit ce qu'on lui a repris.
type consigneFactice struct {
	valeurs  map[string][]byte
	reprises []string
}

func (c *consigneFactice) Stash(_ context.Context, cle string, v []byte) error {
	if c.valeurs == nil {
		c.valeurs = map[string][]byte{}
	}
	c.valeurs[cle] = v
	return nil
}

func (c *consigneFactice) Take(_ context.Context, cle string) ([]byte, error) {
	c.reprises = append(c.reprises, cle)
	v, ok := c.valeurs[cle]
	if !ok {
		return nil, core.ErrNoStash
	}
	delete(c.valeurs, cle)
	return v, nil
}

// runFactice rend UN run, celui de la reprise.
type runFactice struct{ run core.Run }

func (r runFactice) RunByRef(_ context.Context, ref string) (core.Run, error) {
	if r.run.RunRef != ref {
		return core.Run{}, errPortNonUtilise
	}
	return r.run, nil
}
func (runFactice) ListRuns(context.Context, core.RunFilter) (core.RunListResult, error) {
	return core.RunListResult{}, errPortNonUtilise
}
func (runFactice) RunsByHost(context.Context, string, time.Time, time.Time) ([]core.RunListItem, error) {
	return nil, errPortNonUtilise
}
func (runFactice) PreviousRun(context.Context, core.Run) (core.Run, error) {
	return core.Run{}, errPortNonUtilise
}

// inventaireFactice dit si une machine est déclarée.
type inventaireFactice struct{ connues map[string]bool }

func (i inventaireFactice) SaveHost(context.Context, core.ManagedHost) error { return nil }
func (i inventaireFactice) HostByName(_ context.Context, env, nom string) (core.ManagedHost, error) {
	if i.connues[env+"/"+nom] {
		return core.ManagedHost{Environment: env, Name: nom}, nil
	}
	return core.ManagedHost{}, core.ErrNoHost
}
func (i inventaireFactice) ListHosts(context.Context, string) ([]core.ManagedHost, error) {
	return nil, nil
}
func (i inventaireFactice) ForgetHost(context.Context, string, string) error { return nil }

const refEnrolement = "01JZQ000000000000000000ABC"

// cibleDEssai est la cible que la charge et le run existant décrivent tous deux.
func cibleDEssai() *core.EnrollTarget {
	return &core.EnrollTarget{Host: "db-01", Address: "10.0.0.9", Port: 22, Login: "ansible"}
}

// reprendre monte un serveur dont la création échoue toujours, et rejoue la
// requête de reprise contre le run existant décrit par `existant`.
func reprendre(t *testing.T, existant core.Run, declarees map[string]bool, appelant *core.User) (*httptest.ResponseRecorder, *consigneFactice) {
	t.Helper()

	consigne := &consigneFactice{}
	s, err := New(&Config{Addr: "127.0.0.1:0"}, Deps{
		Runs:  runFactice{run: existant},
		Users: stubPort{}, Diffs: stubPort{}, Events: stubPort{},
		Creator: createurSature{},
		Starter: &lanceurFactice{},
		Stash:   consigne,
		Hosts:   inventaireFactice{connues: declarees},
	})
	if err != nil {
		t.Fatalf("api.New : %v", err)
	}

	corps := `{"run_ref":"` + refEnrolement + `","environment":"prod",` +
		`"host":"db-01","address":"10.0.0.9","login":"ansible","password":"x"}`

	w := httptest.NewRecorder()
	s.HandleCreateEnrollment(w, requete(t, http.MethodPost, "/admin/enrollments", appelant, corps))
	return w, consigne
}

func runExistant(etat core.RunState, proprietaire int64) core.Run {
	return core.Run{
		ID: 7, RunRef: refEnrolement, UserID: proprietaire,
		Environment: "prod", Kind: core.RunEnrollment,
		State: etat, EnrollTarget: cibleDEssai(),
	}
}

// LE cas qui a motivé ce fichier, rencontré sur le cluster.
//
// L'enrôlement a RÉUSSI — donc la clé est posée sur la machine — et seule la
// confirmation d'empreinte a été refusée. Reprendre renvoyait alors un 500
// « could not record the enrollment », dont la cause réelle était une violation
// de contrainte PostgreSQL. Le travail restant tient en une déclaration, et
// c'est cela qu'il faut dire.
func TestUneRepriseApresRefusDeDeclarationDitQuIlNeResteQueLInventaire(t *testing.T) {
	t.Parallel()

	w, _ := reprendre(t, runExistant(core.RunSucceeded, 1), nil, administrateur())

	if w.Code != http.StatusConflict {
		t.Fatalf("code = %d, attendu %d\n%s", w.Code, http.StatusConflict, w.Body.String())
	}
	corps := w.Body.String()
	for _, attendu := range []string{"already completed", "db-01", "warden host declare"} {
		if !strings.Contains(corps, attendu) {
			t.Errorf("le message ne dit pas %q : %s", attendu, corps)
		}
	}
}

// La consigne est REPRISE, donc détruite. Sans cela, le mot de passe d'un humain
// resterait dans un Secret que plus aucun pod ne viendrait lire.
func TestUneRepriseRefuseeNeLaissePasLeMotDePasseEnConsigne(t *testing.T) {
	t.Parallel()

	_, consigne := reprendre(t, runExistant(core.RunSucceeded, 1), nil, administrateur())

	if len(consigne.reprises) != 1 || consigne.reprises[0] != refEnrolement {
		t.Fatalf("consigne non reprise : %v", consigne.reprises)
	}
	if len(consigne.valeurs) != 0 {
		t.Errorf("une valeur est restée en consigne : %v", consigne.valeurs)
	}
}

// Une machine DÉJÀ déclarée ne doit pas envoyer l'utilisateur la déclarer une
// seconde fois : il n'y a plus rien à faire, et le dire évite un aller-retour.
func TestUneRepriseSurUneMachineDejaDeclareeLeDit(t *testing.T) {
	t.Parallel()

	w, _ := reprendre(t, runExistant(core.RunSucceeded, 1),
		map[string]bool{"prod/db-01": true}, administrateur())

	corps := w.Body.String()
	if w.Code != http.StatusConflict || !strings.Contains(corps, "is declared") {
		t.Fatalf("code=%d corps=%s", w.Code, corps)
	}
	if strings.Contains(corps, "warden host declare") {
		t.Error("on envoie déclarer une machine qui l'est déjà : " + corps)
	}
}

// Un enrôlement EN COURS ne se relance pas : il se suit.
func TestUneRepriseSurUnEnrolementEnCoursRenvoieALeSuivre(t *testing.T) {
	t.Parallel()

	w, _ := reprendre(t, runExistant(core.RunRunning, 1), nil, administrateur())

	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "already running") {
		t.Fatalf("code=%d corps=%s", w.Code, w.Body.String())
	}
}

// Un run terminé AUTREMENT qu'en succès ne dit pas que la clé est posée — elle
// ne l'est peut-être pas. Le seul conseil honnête est de recommencer.
func TestUneRepriseSurUnEnrolementEchoueNePrometPasQueLaCleEstPosee(t *testing.T) {
	t.Parallel()

	w, _ := reprendre(t, runExistant(core.RunFailed, 1), nil, administrateur())

	corps := w.Body.String()
	if w.Code != http.StatusConflict || !strings.Contains(corps, "already ended") {
		t.Fatalf("code=%d corps=%s", w.Code, corps)
	}
	if strings.Contains(corps, "the key is installed") {
		t.Error("un enrôlement échoué ne doit rien promettre sur la clé : " + corps)
	}
}

// L'endpoint ne DÉCRIT pas le run de quelqu'un d'autre.
//
// Même raison que le 404 de `requireRunAccess` : confirmer l'état, la cible et
// l'environnement d'un run sur la seule présentation de son identifiant ferait
// de cet endpoint un oracle. Le message générique, lui, est vrai pour tout le
// monde.
func TestUneRepriseSurLeRunDUnAutreNeDecritRien(t *testing.T) {
	t.Parallel()

	autre := &core.User{ID: 42, DisplayName: "Mallory", IsAdmin: true}
	w, _ := reprendre(t, runExistant(core.RunSucceeded, 1), nil, autre)

	corps := w.Body.String()
	if w.Code != http.StatusConflict {
		t.Fatalf("code = %d", w.Code)
	}
	for _, interdit := range []string{"db-01", "already completed", "the key is installed"} {
		if strings.Contains(corps, interdit) {
			t.Errorf("le run d'autrui est décrit (%q) : %s", interdit, corps)
		}
	}
}

// La distinction se fait sur l'ERREUR et non sur le texte : un échec ordinaire
// de création reste un 500. Confondre les deux ferait passer une panne pour un
// conflit, et l'appelant réessaierait avec un identifiant neuf sans jamais voir
// le vrai problème.
func TestUnEchecOrdinaireDeCreationResteUneErreurServeur(t *testing.T) {
	t.Parallel()

	s, err := New(&Config{Addr: "127.0.0.1:0"}, Deps{
		Runs: stubPort{}, Users: stubPort{}, Diffs: stubPort{}, Events: stubPort{},
		Creator: createurEnPanne{}, Starter: &lanceurFactice{}, Stash: &consigneFactice{},
	})
	if err != nil {
		t.Fatalf("api.New : %v", err)
	}

	corps := `{"run_ref":"` + refEnrolement + `","environment":"prod",` +
		`"host":"db-01","address":"10.0.0.9","login":"ansible","password":"x"}`
	w := httptest.NewRecorder()
	s.HandleCreateEnrollment(w, requete(t, http.MethodPost, "/admin/enrollments", administrateur(), corps))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d, attendu 500 : %s", w.Code, w.Body.String())
	}
}

type createurEnPanne struct{}

func (createurEnPanne) CreateRun(context.Context, core.NewRun) (int64, error) {
	return 0, errors.New("la base est tombée")
}

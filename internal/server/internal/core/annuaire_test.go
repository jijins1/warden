// Étage 1 de l'ADR-0010 : la connexion par l'annuaire (ADR-0053), sans AD.
//
// Ce qui est protégé ici est l'ORDRE — annuaire, puis compte local — et ce que
// chaque issue de l'annuaire laisse faire au bris de glace.

package core_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/jijins1/warden/internal/server/internal/core"
)

// annuaireFactice double l'Active Directory.
type annuaireFactice struct {
	mu      sync.Mutex
	comptes map[string]string // login -> mot de passe
	id      core.IdentiteAnnuaire
	panne   error
	appels  int
}

func (a *annuaireFactice) Authentifier(_ context.Context, login string, mdp []byte) (core.IdentiteAnnuaire, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.appels++
	if a.panne != nil {
		return core.IdentiteAnnuaire{}, a.panne
	}
	if attendu, ok := a.comptes[login]; !ok || attendu != string(mdp) {
		return core.IdentiteAnnuaire{}, core.ErrIdentifiantsRefuses
	}
	return a.id, nil
}

// provisionFactice double UpsertUser et la liste des groupes d'administration.
type provisionFactice struct {
	recu     []core.Identity
	desactif bool
	err      error
	admins   []string
}

func (p *provisionFactice) UpsertUser(_ context.Context, id core.Identity) (core.User, error) {
	p.recu = append(p.recu, id)
	if p.err != nil {
		return core.User{}, p.err
	}
	return core.User{ID: 42, Type: id.Type, Subject: id.Subject, DisplayName: id.DisplayName, Disabled: p.desactif}, nil
}

func (p *provisionFactice) GroupesAdmin(context.Context) ([]string, error) { return p.admins, nil }

const guidAlice = "3f2504e0-4f89-11d3-9a0c-0305e82c3301"

func avecAnnuaire(t *testing.T, c core.CredentialStore, s core.SessionStore, an core.Annuaire, p core.ComptesAnnuaire) *core.Authenticator {
	t.Helper()
	a := authentificateur(t, c, s)
	if err := a.BrancherLAnnuaire(an, p, 0); err != nil {
		t.Fatalf("BrancherLAnnuaire: %v", err)
	}
	return a
}

func annuaireDAlice() *annuaireFactice {
	return &annuaireFactice{
		comptes: map[string]string{"alice": "mot de passe AD"},
		id: core.IdentiteAnnuaire{
			Subject: guidAlice, DisplayName: "Alice Martin",
			Groupes: []string{"GG-Warden-Prod", "gg-warden-prod", " GG-Equipe-Infra ", ""},
		},
	}
}

func TestUnCompteADOuvreUneSessionCourteAvecSesGroupesNormalises(t *testing.T) {
	t.Parallel()

	sess := nouvellesSessions()
	prov := &provisionFactice{}
	a := avecAnnuaire(t, nouveauxComptes(t, "breakglass", motDePasseValide), sess, annuaireDAlice(), prov)

	avant := time.Now()
	o, err := a.Login(context.Background(), "alice", []byte("mot de passe AD"))
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	// Le sujet est le GUID, pas le login : un login se renomme (ADR-0021).
	if len(prov.recu) != 1 || prov.recu[0].Type != core.UserLDAP || prov.recu[0].Subject != guidAlice {
		t.Fatalf("provisionné : %+v, attendu un compte ldap de sujet %s", prov.recu, guidAlice)
	}
	if got := o.ExpiresAt.Sub(avant); got > core.DureeSessionAnnuaire+time.Minute || got < core.DureeSessionAnnuaire-time.Minute {
		t.Errorf("session de %s, %s attendues : la durée borne le retrait d'un groupe", got, core.DureeSessionAnnuaire)
	}
	want := []string{"gg-equipe-infra", "gg-warden-prod"}
	if len(sess.groupes) != 1 || !reflect.DeepEqual(sess.groupes[0], want) {
		t.Fatalf("groupes de la session = %v, attendu %v", sess.groupes, want)
	}
}

// Un compte local actif est vérifié sans interroger l'annuaire.
func TestUnCompteLocalEstVerifieSansInterrogerLAnnuaire(t *testing.T) {
	t.Parallel()

	sess := nouvellesSessions()
	an := annuaireDAlice()
	a := avecAnnuaire(t, nouveauxComptes(t, "breakglass", motDePasseValide), sess, an, &provisionFactice{})

	o, err := a.Login(context.Background(), "breakglass", []byte(motDePasseValide))
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if an.appels != 0 {
		t.Errorf("annuaire appelé %d fois pour un compte local", an.appels)
	}
	if o.User.Type != core.UserLocal {
		t.Errorf("compte ouvert de type %s, local attendu", o.User.Type)
	}
	if len(sess.groupes) != 1 || sess.groupes[0] != nil {
		t.Errorf("un compte local ne porte aucun groupe, reçu %v", sess.groupes)
	}
}

// Un mauvais mot de passe local est un refus, pas une invitation à essayer l'AD :
// sinon un compte local homonyme ne protégerait rien.
func TestUnMauvaisMotDePasseLocalNeRetombePasSurLAnnuaire(t *testing.T) {
	t.Parallel()

	an := annuaireDAlice()
	a := avecAnnuaire(t, nouveauxComptes(t, "alice", motDePasseValide), nouvellesSessions(), an, &provisionFactice{})

	_, err := a.Login(context.Background(), "alice", []byte("mot de passe AD"))
	if !errors.Is(err, core.ErrIdentifiantsRefuses) {
		t.Fatalf("erreur = %v, ErrIdentifiantsRefuses attendue", err)
	}
	if an.appels != 0 {
		t.Fatalf("annuaire appelé %d fois après un refus local", an.appels)
	}
}

// Le bris de glace : un compte local marche même quand l'AD est en panne,
// puisqu'il est vérifié avant.
func TestUnCompteLocalMarchePendantUnePanneDeLAnnuaire(t *testing.T) {
	t.Parallel()

	an := &annuaireFactice{panne: errors.New("connexion refusée")}
	a := avecAnnuaire(t, nouveauxComptes(t, "breakglass", motDePasseValide), nouvellesSessions(), an, &provisionFactice{})

	if _, err := a.Login(context.Background(), "breakglass", []byte(motDePasseValide)); err != nil {
		t.Fatalf("compte local refusé pendant une panne de l'AD : %v", err)
	}
}

// Un compte local DÉSACTIVÉ compte comme absent : c'est ainsi qu'une personne
// passe à l'AD sans rien supprimer.
func TestUnCompteLocalDesactiveLaissePasserLAnnuaire(t *testing.T) {
	t.Parallel()

	comptes := nouveauxComptes(t, "alice", motDePasseValide)
	u := comptes.users["alice"]
	u.Disabled = true
	comptes.users["alice"] = u

	a := avecAnnuaire(t, comptes, nouvellesSessions(), annuaireDAlice(), &provisionFactice{})

	o, err := a.Login(context.Background(), "alice", []byte("mot de passe AD"))
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if o.User.Type != core.UserLDAP {
		t.Fatalf("compte ouvert de type %s, ldap attendu", o.User.Type)
	}
}

// Une panne qui se présenterait en 401 passerait des heures pour une vague de
// mots de passe faux.
func TestUnePanneDeLAnnuaireSansCompteLocalEstUnePanneEtNonUnRefus(t *testing.T) {
	t.Parallel()

	an := &annuaireFactice{panne: errors.New("connexion refusée")}
	a := avecAnnuaire(t, nouveauxComptes(t, "breakglass", motDePasseValide), nouvellesSessions(), an, &provisionFactice{})

	_, err := a.Login(context.Background(), "alice", []byte("mot de passe AD"))
	if !errors.Is(err, core.ErrAnnuaireIndisponible) {
		t.Fatalf("erreur = %v, ErrAnnuaireIndisponible attendue", err)
	}
	if errors.Is(err, core.ErrIdentifiantsRefuses) {
		t.Fatal("la panne se présente aussi comme un refus")
	}
}

func TestUnMauvaisMotDePasseADEstUnRefusOrdinaire(t *testing.T) {
	t.Parallel()

	a := avecAnnuaire(t, nouveauxComptes(t, "breakglass", motDePasseValide), nouvellesSessions(), annuaireDAlice(), &provisionFactice{})

	_, err := a.Login(context.Background(), "alice", []byte("faux"))
	if !errors.Is(err, core.ErrIdentifiantsRefuses) {
		t.Fatalf("erreur = %v, ErrIdentifiantsRefuses attendue", err)
	}
}

// Désactiver le compte côté warden coupe l'accès sans attendre l'AD.
func TestUnCompteADDesactiveDansWardenEstRefuse(t *testing.T) {
	t.Parallel()

	sess := nouvellesSessions()
	a := avecAnnuaire(t, nouveauxComptes(t, "breakglass", motDePasseValide), sess, annuaireDAlice(), &provisionFactice{desactif: true})

	_, err := a.Login(context.Background(), "alice", []byte("mot de passe AD"))
	if !errors.Is(err, core.ErrIdentifiantsRefuses) {
		t.Fatalf("erreur = %v, ErrIdentifiantsRefuses attendue", err)
	}
	if sess.creees != 0 {
		t.Fatalf("%d session(s) ouverte(s) pour un compte désactivé", sess.creees)
	}
}

// Le provisionnement qui échoue après un bind réussi est une PANNE : ni un
// refus, ni une panne d'annuaire, et aucune session.
func TestUnProvisionnementEnEchecNOuvreAucuneSession(t *testing.T) {
	t.Parallel()

	sess := nouvellesSessions()
	prov := &provisionFactice{err: errors.New("base indisponible")}
	a := avecAnnuaire(t, nouveauxComptes(t, "breakglass", motDePasseValide), sess, annuaireDAlice(), prov)

	_, err := a.Login(context.Background(), "alice", []byte("mot de passe AD"))
	if err == nil || errors.Is(err, core.ErrIdentifiantsRefuses) || errors.Is(err, core.ErrAnnuaireIndisponible) {
		t.Fatalf("erreur = %v, une panne de provisionnement attendue", err)
	}
	if sess.creees != 0 {
		t.Fatalf("%d session(s) ouverte(s) sans compte provisionné", sess.creees)
	}
}

// Le login et chaque requête disent la MÊME chose de l'administration : la
// règle est dans le cœur, appliquée aux deux.
func TestLAdministrationParGroupeEstLaMemeAuLoginEtALaRequete(t *testing.T) {
	t.Parallel()

	sess := nouvellesSessions()
	prov := &provisionFactice{admins: []string{"gg-warden-prod"}}
	a := avecAnnuaire(t, nouveauxComptes(t, "breakglass", motDePasseValide), sess, annuaireDAlice(), prov)

	o, err := a.Login(context.Background(), "alice", []byte("mot de passe AD"))
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if !o.User.IsAdmin {
		t.Fatal("le login ne dit pas administrateur un membre d'un groupe de `admins`")
	}

	sess.user = core.User{ID: 42, Type: core.UserLDAP, Subject: guidAlice}
	u, _, err := a.Authentifier(context.Background(), o.Token)
	if err != nil {
		t.Fatalf("Authentifier: %v", err)
	}
	if !u.IsAdmin {
		t.Fatal("la requête ne dit pas administrateur un membre d'un groupe de `admins`")
	}

	// Le retrait de `admins` prend effet à la requête suivante.
	prov.admins = nil
	if u, _, _ = a.Authentifier(context.Background(), o.Token); u.IsAdmin {
		t.Fatal("administration toujours ouverte après le retrait du groupe")
	}
}

func TestEstAdministrateurParGroupe(t *testing.T) {
	t.Parallel()

	if core.EstAdministrateurParGroupe(nil, []string{"gg-admin"}) {
		t.Error("une session sans groupe est administratrice")
	}
	if !core.EstAdministrateurParGroupe([]string{"gg-dev", "gg-admin"}, []string{"gg-admin"}) {
		t.Error("un membre de gg-admin n'est pas administrateur")
	}
}

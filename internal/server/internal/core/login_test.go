// Étage 1 de l'ADR-0010 : ni base, ni cluster.
//
// Ce que ces tests protègent n'est pas « la connexion marche » — c'est ce qu'elle
// refuse de DIRE. Un endpoint de connexion divulgue par ses erreurs, par son
// temps de réponse et par sa consommation ; les trois sont vérifiés ici.

package core_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/argon2"

	"github.com/jijins1/warden/internal/server/internal/core"
)

const motDePasseValide = "un mot de passe assez long"

// comptesFactices double le magasin des comptes locaux.
type comptesFactices struct {
	mu        sync.Mutex
	empreinte map[string]string // sujet -> empreinte
	users     map[string]core.User
	err       error

	poses int
}

func nouveauxComptes(t *testing.T, sujet, motDePasse string) *comptesFactices {
	t.Helper()
	e, err := core.Empreinte([]byte(motDePasse))
	if err != nil {
		t.Fatalf("Empreinte: %v", err)
	}
	return &comptesFactices{
		empreinte: map[string]string{sujet: e},
		users: map[string]core.User{
			sujet: {ID: 7, Type: core.UserLocal, Subject: sujet, DisplayName: sujet},
		},
	}
}

func (c *comptesFactices) EmpreinteDuCompte(_ context.Context, sujet string) (core.User, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return core.User{}, "", c.err
	}
	e, ok := c.empreinte[sujet]
	if !ok {
		return core.User{}, "", core.ErrNotFound
	}
	return c.users[sujet], e, nil
}

func (c *comptesFactices) PoserMotDePasse(_ context.Context, _ int64, empreinte string, _ int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.poses++
	c.empreinte["alice"] = empreinte
	return nil
}

// sessionsFactices double le magasin des sessions.
type sessionsFactices struct {
	mu       sync.Mutex
	creees   int
	revoquee []int64
	touchees int
	err      error

	// vivante est ce que SessionByToken rend, indexé par empreinte.
	vivante map[string]core.Session
	user    core.User

	// groupes retient ce que chaque session a reçu, dans l'ordre d'ouverture.
	groupes [][]string
}

func nouvellesSessions() *sessionsFactices {
	return &sessionsFactices{vivante: map[string]core.Session{}}
}

func (s *sessionsFactices) CreateSession(_ context.Context, userID int64, hash []byte, expire time.Time, groupes []string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return 0, s.err
	}
	s.groupes = append(s.groupes, groupes)
	s.creees++
	id := int64(s.creees)
	s.vivante[string(hash)] = core.Session{ID: id, UserID: userID, ExpiresAt: expire, Groupes: groupes}
	return id, nil
}

func (s *sessionsFactices) SessionByToken(_ context.Context, hash []byte) (core.Session, core.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.vivante[string(hash)]
	if !ok {
		return core.Session{}, core.User{}, core.ErrNotFound
	}
	return sess, s.user, nil
}

func (s *sessionsFactices) RevokeSession(_ context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revoquee = append(s.revoquee, id)
	return nil
}

func (s *sessionsFactices) TouchSession(_ context.Context, _ int64, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.touchees++
	return nil
}

func authentificateur(t *testing.T, c core.CredentialStore, s core.SessionStore) *core.Authenticator {
	t.Helper()
	a, err := core.NewAuthenticator(c, s, 0)
	if err != nil {
		t.Fatalf("NewAuthenticator: %v", err)
	}
	return a
}

// --- l'empreinte ---

func TestUnMotDePasseSeVerifieContreSonEmpreinte(t *testing.T) {
	t.Parallel()

	e, err := core.Empreinte([]byte(motDePasseValide))
	if err != nil {
		t.Fatalf("Empreinte: %v", err)
	}

	ok, err := core.VerifierMotDePasse(e, []byte(motDePasseValide))
	if err != nil || !ok {
		t.Errorf("le bon mot de passe a été refusé (ok=%v err=%v)", ok, err)
	}

	ok, err = core.VerifierMotDePasse(e, []byte("un mot de passe assez lonh"))
	if err != nil {
		t.Fatalf("VerifierMotDePasse: %v", err)
	}
	if ok {
		t.Error("un mauvais mot de passe a été accepté")
	}
}

// Deux empreintes du MÊME mot de passe doivent différer : sans sel distinct,
// deux comptes ayant choisi le même mot de passe se reconnaissent en base, et
// une table précalculée les casse tous les deux d'un coup.
func TestDeuxEmpreintesDuMemeMotDePasseDifferent(t *testing.T) {
	t.Parallel()

	a, err := core.Empreinte([]byte(motDePasseValide))
	if err != nil {
		t.Fatalf("Empreinte: %v", err)
	}
	b, err := core.Empreinte([]byte(motDePasseValide))
	if err != nil {
		t.Fatalf("Empreinte: %v", err)
	}
	if a == b {
		t.Error("deux empreintes identiques : le sel n'est pas tiré au hasard")
	}
}

// Les PARAMÈTRES voyagent dans l'empreinte. C'est ce qui permettra de durcir le
// coût de calcul sans invalider ce qui est déjà en base.
func TestLEmpreintePorteSesParametres(t *testing.T) {
	t.Parallel()

	e, err := core.Empreinte([]byte(motDePasseValide))
	if err != nil {
		t.Fatalf("Empreinte: %v", err)
	}
	if !strings.HasPrefix(e, "$argon2id$v=19$m=") {
		t.Errorf("empreinte = %q : le format PHC attendu par le schéma n'est pas respecté", e)
	}

	// LA propriété : une empreinte produite avec d'AUTRES paramètres reste
	// vérifiable. C'est ce qui permettra de durcir le coût de calcul sans
	// invalider ce qui est déjà en base — et c'est ce qui casserait si la
	// vérification relisait les constantes au lieu de la chaîne.
	//
	// Les paramètres sont volontairement dérisoires (8 Kio, une passe) : le test
	// porte sur leur relecture, pas sur leur solidité.
	sel := []byte("un sel de seize.")
	brut := argon2.IDKey([]byte(motDePasseValide), sel, 1, 8, 1, 32)
	b64 := base64.RawStdEncoding.EncodeToString
	ancienne := fmt.Sprintf("$argon2id$v=19$m=8,t=1,p=1$%s$%s", b64(sel), b64(brut))

	ok, err := core.VerifierMotDePasse(ancienne, []byte(motDePasseValide))
	if err != nil {
		t.Fatalf("une empreinte aux paramètres différents est illisible : %v", err)
	}
	if !ok {
		t.Error("une empreinte aux paramètres différents a été refusée : " +
			"durcir le coût de calcul déconnecterait tout le monde")
	}
}

// marshal rend la forme JSON, pour vérifier qu'un secret n'y apparaît pas.
func marshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return string(b)
}

func TestUneEmpreinteIllisibleNEstPasUnMauvaisMotDePasse(t *testing.T) {
	t.Parallel()

	for nom, e := range map[string]string{
		"vide":             "",
		"pas argon2id":     "$argon2i$v=19$m=1,t=1,p=1$c2Vs$ZW1wcmVpbnRl",
		"version inconnue": "$argon2id$v=16$m=1,t=1,p=1$c2Vs$ZW1wcmVpbnRl",
		"paramètres nuls":  "$argon2id$v=19$m=0,t=1,p=1$c2Vs$ZW1wcmVpbnRl",
		"sel non base64":   "$argon2id$v=19$m=1,t=1,p=1$!!!$ZW1wcmVpbnRl",
		"champs manquants": "$argon2id$v=19$m=1,t=1,p=1$c2Vs",
		"empreinte vide":   "$argon2id$v=19$m=1,t=1,p=1$c2Vs$",
		"texte quelconque": "pas une empreinte du tout",
	} {
		t.Run(nom, func(t *testing.T) {
			t.Parallel()
			ok, err := core.VerifierMotDePasse(e, []byte(motDePasseValide))
			if ok {
				t.Fatal("une empreinte illisible a validé un mot de passe")
			}
			if !errors.Is(err, core.ErrEmpreinteIllisible) {
				t.Errorf("err = %v, attendu ErrEmpreinteIllisible : une corruption "+
					"de données se présenterait comme un utilisateur qui se trompe", err)
			}
		})
	}
}

// Aucune règle de composition, une seule de longueur (NIST SP 800-63B).
func TestUnMotDePasseTropCourtEstRefuse(t *testing.T) {
	t.Parallel()

	if _, err := core.Empreinte([]byte("court")); !errors.Is(err, core.ErrMotDePasseFaible) {
		t.Errorf("err = %v, attendu ErrMotDePasseFaible", err)
	}
	// Douze caractères sans chiffre ni majuscule : accepté, délibérément.
	if _, err := core.Empreinte([]byte("douzecaracte")); err != nil {
		t.Errorf("un mot de passe de longueur suffisante a été refusé : %v", err)
	}
}

// L'empreinte leurre doit être VÉRIFIABLE : si elle était malformée, le chemin
// du compte inconnu rendrait une erreur au lieu d'un refus, et se distinguerait
// donc du chemin du mauvais mot de passe — ce qu'elle existe pour empêcher.
func TestLEmpreinteLeurreEstUneVraieEmpreinte(t *testing.T) {
	t.Parallel()

	ok, err := core.VerifierMotDePasse(core.EmpreinteLeurre, []byte(motDePasseValide))
	if err != nil {
		t.Fatalf("l'empreinte leurre est illisible : %v", err)
	}
	if ok {
		t.Error("un mot de passe correspond à l'empreinte leurre")
	}
}

// --- le jeton de session ---

func TestUnJetonDeSessionNeSImprimePas(t *testing.T) {
	t.Parallel()

	tok, err := core.NewSessionToken()
	if err != nil {
		t.Fatalf("NewSessionToken: %v", err)
	}
	clair := tok.Reveal()

	for nom, rendu := range map[string]string{
		"%v": fmt.Sprintf("%v", tok),
		// `fmt.Sprintf` et non `tok.String()` : ce test vérifie que le VERBE `%s`
		// ne fuit pas, c'est-à-dire le chemin qu'emprunte un `slog` ou un
		// `t.Logf` distrait. Appeler String() directement testerait une autre
		// fonction que celle qui pose le risque.
		//nolint:staticcheck // S1025 : la substitution proposée annulerait le test.
		"%s":     fmt.Sprintf("%s", tok),
		"%#v":    fmt.Sprintf("%#v", tok),
		"JSON":   marshal(t, tok),
		"struct": fmt.Sprintf("%v", struct{ T core.SessionToken }{tok}),
	} {
		if strings.Contains(rendu, clair) {
			t.Errorf("%s laisse fuir le jeton : %s", nom, rendu)
		}
	}
}

func TestUnJetonMalformeEstRefuseSansToucherLaBase(t *testing.T) {
	t.Parallel()

	for _, s := range []string{"", "trop-court", strings.Repeat("a", 100), "!!!"} {
		if _, err := core.ParseSessionToken(s); !errors.Is(err, core.ErrJetonInvalide) {
			t.Errorf("ParseSessionToken(%q) = %v, attendu ErrJetonInvalide", s, err)
		}
	}
}

func TestDeuxJetonsDiffèrent(t *testing.T) {
	t.Parallel()

	vus := map[string]struct{}{}
	for range 50 {
		tok, err := core.NewSessionToken()
		if err != nil {
			t.Fatalf("NewSessionToken: %v", err)
		}
		if _, deja := vus[tok.Reveal()]; deja {
			t.Fatal("deux jetons identiques")
		}
		vus[tok.Reveal()] = struct{}{}
	}
}

// --- la connexion ---

func TestUneConnexionValideOuvreUneSession(t *testing.T) {
	t.Parallel()

	c := nouveauxComptes(t, "alice", motDePasseValide)
	s := nouvellesSessions()

	o, err := authentificateur(t, c, s).Login(t.Context(), "alice", []byte(motDePasseValide))
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if o.User.Subject != "alice" {
		t.Errorf("utilisateur = %+v", o.User)
	}
	if o.Token.Zero() {
		t.Error("aucun jeton rendu")
	}
	if s.creees != 1 {
		t.Errorf("sessions créées = %d, attendu 1", s.creees)
	}
	if !o.ExpiresAt.After(time.Now()) {
		t.Errorf("expiration = %v : déjà passée", o.ExpiresAt)
	}
}

// LE test qui compte. Compte inconnu, mauvais mot de passe et compte désactivé
// doivent rendre la MÊME erreur : les distinguer confirmerait à qui essaie qu'un
// compte existe.
func TestToutesLesIssuesNegativesSeRessemblent(t *testing.T) {
	t.Parallel()

	c := nouveauxComptes(t, "alice", motDePasseValide)
	c.users["bob"] = core.User{ID: 8, Subject: "bob", Disabled: true}
	e, _ := core.Empreinte([]byte(motDePasseValide))
	c.empreinte["bob"] = e

	a := authentificateur(t, c, nouvellesSessions())

	cas := map[string]struct{ sujet, mdp string }{
		"compte inconnu":       {"charlie", motDePasseValide},
		"mauvais mot de passe": {"alice", "un autre mot de passe long"},
		"compte désactivé":     {"bob", motDePasseValide},
	}
	for nom, k := range cas {
		t.Run(nom, func(t *testing.T) {
			_, err := a.Login(t.Context(), k.sujet, []byte(k.mdp))
			if !errors.Is(err, core.ErrIdentifiantsRefuses) {
				t.Errorf("err = %v, attendu ErrIdentifiantsRefuses", err)
			}
		})
	}
}

// Le compte inconnu doit coûter le MÊME temps qu'un compte existant : sinon
// l'endpoint de connexion énumère les comptes du produit au chronomètre.
//
// Le test ne mesure pas la durée — elle est trop bruitée en CI. Il vérifie la
// propriété qui la produit : qu'aucune session n'est créée et que le refus passe
// par le même chemin, ce que garantit l'empreinte leurre (testée plus haut).
func TestUnCompteInconnuNOuvreRienEtNeSeDistinguePas(t *testing.T) {
	t.Parallel()

	c := nouveauxComptes(t, "alice", motDePasseValide)
	s := nouvellesSessions()

	_, err := authentificateur(t, c, s).Login(t.Context(), "personne", []byte(motDePasseValide))
	if !errors.Is(err, core.ErrIdentifiantsRefuses) {
		t.Errorf("err = %v", err)
	}
	if s.creees != 0 {
		t.Error("une session a été ouverte pour un compte inconnu")
	}
}

// Le délai plancher uniformise le temps de réponse ET borne le martelage.
func TestUneTentativeNeRepondPasAvantLePlancher(t *testing.T) {
	t.Parallel()

	c := nouveauxComptes(t, "alice", motDePasseValide)
	a := authentificateur(t, c, nouvellesSessions())

	debut := time.Now()
	_, _ = a.Login(t.Context(), "personne", []byte("mauvais mot de passe"))
	ecoule := time.Since(debut)

	if ecoule < core.DelaiPlancher {
		t.Errorf("réponse en %v, plancher %v : le martelage n'est pas freiné",
			ecoule, core.DelaiPlancher)
	}
}

// Un appelant déjà parti ne doit pas occuper une place de la file : le frein se
// retournerait contre le produit.
func TestUnContexteAnnuleNAttendPasLePlancher(t *testing.T) {
	t.Parallel()

	c := nouveauxComptes(t, "alice", motDePasseValide)
	a := authentificateur(t, c, nouvellesSessions())

	ctx, annuler := context.WithCancel(context.Background())
	annuler()

	debut := time.Now()
	_, err := a.Login(ctx, "alice", []byte(motDePasseValide))

	// L'ERREUR d'abord, parce qu'elle est déterministe là où la durée ne l'est
	// pas : `context.Canceled` prouve qu'on est sorti sans prendre de place ni
	// dériver quoi que ce soit. Sans elle, ce test ne tenait que par le fait
	// qu'Argon2id restait sous les 250 ms — donc pas sur une machine chargée.
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("erreur %v, voulu context.Canceled : l'appel a travaillé "+
			"alors que l'appelant était déjà parti", err)
	}
	if ecoule := time.Since(debut); ecoule >= core.DelaiPlancher {
		t.Errorf("attente de %v sur un contexte annulé", ecoule)
	}
}

// Une empreinte corrompue en base ne doit PAS se lire comme un mauvais mot de
// passe : sans cette distinction, une corruption se présente des mois durant
// comme un utilisateur qui se trompe de touche.
func TestUneEmpreinteCorrompueRemonteCommeUnDefaut(t *testing.T) {
	t.Parallel()

	c := nouveauxComptes(t, "alice", motDePasseValide)
	c.empreinte["alice"] = "$argon2id$corrompu"

	_, err := authentificateur(t, c, nouvellesSessions()).
		Login(t.Context(), "alice", []byte(motDePasseValide))

	if errors.Is(err, core.ErrIdentifiantsRefuses) {
		t.Error("une empreinte corrompue s'est présentée comme un refus d'identifiants")
	}
	if !errors.Is(err, core.ErrEmpreinteIllisible) {
		t.Errorf("err = %v, attendu ErrEmpreinteIllisible", err)
	}
}

// --- le changement de mot de passe ---

// L'ancien est exigé même authentifié : un poste laissé déverrouillé suffirait
// sinon à s'approprier le compte de façon DURABLE, là où un jeton expire.
func TestChangerExigeLAncienMotDePasse(t *testing.T) {
	t.Parallel()

	c := nouveauxComptes(t, "alice", motDePasseValide)
	a := authentificateur(t, c, nouvellesSessions())
	alice := c.users["alice"]

	err := a.ChangerMotDePasse(t.Context(), alice, []byte("le mauvais ancien"), []byte("un nouveau mot de passe"))
	if !errors.Is(err, core.ErrIdentifiantsRefuses) {
		t.Errorf("err = %v, attendu ErrIdentifiantsRefuses", err)
	}
	if c.poses != 0 {
		t.Error("le mot de passe a été changé sans que l'ancien soit connu")
	}

	if err := a.ChangerMotDePasse(t.Context(), alice,
		[]byte(motDePasseValide), []byte("un nouveau mot de passe")); err != nil {
		t.Fatalf("ChangerMotDePasse: %v", err)
	}
	if c.poses != 1 {
		t.Errorf("poses = %d, attendu 1", c.poses)
	}
}

func TestChangerRefuseUnNouveauMotDePasseFaible(t *testing.T) {
	t.Parallel()

	c := nouveauxComptes(t, "alice", motDePasseValide)
	a := authentificateur(t, c, nouvellesSessions())

	err := a.ChangerMotDePasse(t.Context(), c.users["alice"], []byte(motDePasseValide), []byte("court"))
	if !errors.Is(err, core.ErrMotDePasseFaible) {
		t.Errorf("err = %v, attendu ErrMotDePasseFaible", err)
	}
	if c.poses != 0 {
		t.Error("un mot de passe faible a été écrit")
	}
}

// --- l'authentification par jeton ---

func TestUnJetonVivantResoutSonUtilisateur(t *testing.T) {
	t.Parallel()

	c := nouveauxComptes(t, "alice", motDePasseValide)
	s := nouvellesSessions()
	s.user = c.users["alice"]
	a := authentificateur(t, c, s)

	o, err := a.Login(t.Context(), "alice", []byte(motDePasseValide))
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	u, sess, err := a.Authentifier(t.Context(), o.Token)
	if err != nil {
		t.Fatalf("Authentifier: %v", err)
	}
	if u.Subject != "alice" || sess.ID == 0 {
		t.Errorf("utilisateur = %+v, session = %+v", u, sess)
	}
}

// Un compte désactivé après coup : la désactivation révoque les sessions, mais
// la course existe et le COMPTE doit primer.
func TestUnJetonSurUnCompteDesactiveEstRefuse(t *testing.T) {
	t.Parallel()

	c := nouveauxComptes(t, "alice", motDePasseValide)
	s := nouvellesSessions()
	s.user = c.users["alice"]
	a := authentificateur(t, c, s)

	o, _ := a.Login(t.Context(), "alice", []byte(motDePasseValide))

	s.mu.Lock()
	s.user.Disabled = true
	s.mu.Unlock()

	if _, _, err := a.Authentifier(t.Context(), o.Token); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("err = %v : un compte désactivé garde l'accès par sa session", err)
	}
}

// `last_seen_at` est du confort d'exploitation. Refuser une requête authentifiée
// parce qu'une statistique n'a pas pu s'écrire serait une panne fabriquée.
func TestUnRafraichissementEnEchecNInterditPasLaRequete(t *testing.T) {
	t.Parallel()

	c := nouveauxComptes(t, "alice", motDePasseValide)
	s := &sessionsEnPanneDeTouch{sessionsFactices: nouvellesSessions()}
	s.user = c.users["alice"]
	a := authentificateur(t, c, s)

	o, _ := a.Login(t.Context(), "alice", []byte(motDePasseValide))
	if _, _, err := a.Authentifier(t.Context(), o.Token); err != nil {
		t.Errorf("err = %v : la requête a été refusée pour une statistique", err)
	}
}

type sessionsEnPanneDeTouch struct{ *sessionsFactices }

func (s *sessionsEnPanneDeTouch) TouchSession(context.Context, int64, time.Duration) error {
	return errors.New("base en lecture seule")
}

func TestLogoutRevoqueLaSession(t *testing.T) {
	t.Parallel()

	c := nouveauxComptes(t, "alice", motDePasseValide)
	s := nouvellesSessions()
	s.user = c.users["alice"]
	a := authentificateur(t, c, s)

	o, _ := a.Login(t.Context(), "alice", []byte(motDePasseValide))
	_, sess, _ := a.Authentifier(t.Context(), o.Token)

	if err := a.Logout(t.Context(), sess.ID); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if len(s.revoquee) != 1 || s.revoquee[0] != sess.ID {
		t.Errorf("révoquées = %v, attendu [%d]", s.revoquee, sess.ID)
	}
}

func TestNewAuthenticatorRefuseUnMontageIncomplet(t *testing.T) {
	t.Parallel()

	if _, err := core.NewAuthenticator(nil, nouvellesSessions(), 0); err == nil {
		t.Error("construit sans magasin de comptes")
	}
	if _, err := core.NewAuthenticator(&comptesFactices{}, nil, 0); err == nil {
		t.Error("construit sans magasin de sessions")
	}
}

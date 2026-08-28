// Étage 1 de l'ADR-0010 : ni base, ni cluster.
//
// Ce fichier porte le seul secret que le produit laisse séjourner sur le poste
// de l'utilisateur. Ce que ces tests protègent est donc moins « on sait relire ce
// qu'on a écrit » que : personne d'autre ne peut le lire, une écriture
// interrompue ne perd pas tout, et deux serveurs ne se confondent pas.

package session_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jijins1/warden/internal/cli/internal/session"
)

const serveurA = "https://warden.example"

// isole redirige le répertoire de configuration vers un temporaire.
//
// `XDG_CONFIG_HOME` plutôt qu'une variable inventée pour le test : c'est ce que
// `os.UserConfigDir` lit réellement sous Linux, donc le test exerce le vrai
// chemin de résolution plutôt qu'une porte dérobée.
func isole(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	return dir
}

func session1(sujet string) session.Session {
	return session.Session{
		Token:     "un-jeton",
		Subject:   sujet,
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}
}

func TestUneSessionRangeeSeRelit(t *testing.T) {
	isole(t)

	if err := session.Ranger(serveurA, session1("alice")); err != nil {
		t.Fatalf("Ranger: %v", err)
	}

	s, err := session.Charger(serveurA)
	if err != nil {
		t.Fatalf("Charger: %v", err)
	}
	if s.Token != "un-jeton" || s.Subject != "alice" {
		t.Errorf("session = %+v", s)
	}
}

// L'état INITIAL normal : n'avoir jamais lancé `warden login`. Le message doit
// dire quoi faire, pas quel appel système a échoué.
func TestSansFichierIlNYAPasDeSession(t *testing.T) {
	isole(t)

	if _, err := session.Charger(serveurA); !errors.Is(err, session.ErrPasDeSession) {
		t.Errorf("err = %v, attendu ErrPasDeSession", err)
	}
}

// LE test de ce paquet. Le fichier porte un jeton qui vaut l'identité de son
// propriétaire ; lisible par d'autres, il ne vaut plus rien.
func TestLeFichierEstEcritEn0600(t *testing.T) {
	isole(t)

	if err := session.Ranger(serveurA, session1("alice")); err != nil {
		t.Fatalf("Ranger: %v", err)
	}

	chemin, err := session.Chemin()
	if err != nil {
		t.Fatalf("Chemin: %v", err)
	}
	info, err := os.Stat(chemin)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("mode = %#o, attendu 0600", mode)
	}

	// Le répertoire aussi : un répertoire traversable par d'autres laisse lister
	// et, selon les droits, remplacer le fichier.
	dir, err := os.Stat(filepath.Dir(chemin))
	if err != nil {
		t.Fatalf("Stat du répertoire: %v", err)
	}
	if mode := dir.Mode().Perm(); mode&0o077 != 0 {
		t.Errorf("mode du répertoire = %#o : lisible par d'autres", mode)
	}
}

// Le contrôle est à la LECTURE et pas seulement à l'écriture : un umask
// permissif, une restauration de sauvegarde ou un `cp` sans `-p` relâchent les
// droits, et le fichier continue de fonctionner sans que rien ne le dise.
func TestUnFichierTropOuvertEstRefuse(t *testing.T) {
	isole(t)

	if err := session.Ranger(serveurA, session1("alice")); err != nil {
		t.Fatalf("Ranger: %v", err)
	}
	chemin, _ := session.Chemin()
	if err := os.Chmod(chemin, 0o644); err != nil {
		t.Fatalf("Chmod: %v", err)
	}

	_, err := session.Charger(serveurA)
	if !errors.Is(err, session.ErrDroitsTropLarges) {
		t.Fatalf("err = %v, attendu ErrDroitsTropLarges", err)
	}
	// Le message doit dire COMMENT réparer : découvrir qu'on est refusé sans
	// savoir quoi faire est le pire des deux mondes.
	if !strings.Contains(err.Error(), "chmod 600") {
		t.Errorf("le message ne dit pas comment réparer : %v", err)
	}
}

// On parle à un warden de développement et à un de production. Écraser l'un avec
// l'autre ferait pointer une commande sur le mauvais parc — silencieusement.
func TestDeuxServeursNeSeConfondentPas(t *testing.T) {
	isole(t)

	const serveurB = "https://warden.dev.example"
	if err := session.Ranger(serveurA, session.Session{Token: "jeton-prod", Subject: "alice"}); err != nil {
		t.Fatalf("Ranger A: %v", err)
	}
	if err := session.Ranger(serveurB, session.Session{Token: "jeton-dev", Subject: "alice"}); err != nil {
		t.Fatalf("Ranger B: %v", err)
	}

	a, err := session.Charger(serveurA)
	if err != nil {
		t.Fatalf("Charger A: %v", err)
	}
	b, err := session.Charger(serveurB)
	if err != nil {
		t.Fatalf("Charger B: %v", err)
	}
	if a.Token != "jeton-prod" || b.Token != "jeton-dev" {
		t.Errorf("jetons croisés : A=%q B=%q", a.Token, b.Token)
	}
}

// Sans normalisation, `https://x` et `https://x/` seraient deux entrées, et se
// connecter à l'une laisserait l'autre réclamer une connexion — une confusion
// d'autant plus pénible qu'elle est invisible.
func TestLAdresseEstNormalisee(t *testing.T) {
	isole(t)

	if err := session.Ranger("https://warden.example/", session1("alice")); err != nil {
		t.Fatalf("Ranger: %v", err)
	}
	if _, err := session.Charger("https://warden.example"); err != nil {
		t.Errorf("une barre finale a créé une seconde entrée : %v", err)
	}
	if _, err := session.Charger("  https://warden.example/  "); err != nil {
		t.Errorf("les espaces n'ont pas été ignorés : %v", err)
	}
}

// Oublier un serveur ne doit pas emporter les autres.
func TestOublierNeTouchePasLesAutresServeurs(t *testing.T) {
	isole(t)

	const serveurB = "https://warden.dev.example"
	_ = session.Ranger(serveurA, session.Session{Token: "jeton-prod"})
	_ = session.Ranger(serveurB, session.Session{Token: "jeton-dev"})

	if err := session.Oublier(serveurA); err != nil {
		t.Fatalf("Oublier: %v", err)
	}
	if _, err := session.Charger(serveurA); !errors.Is(err, session.ErrPasDeSession) {
		t.Errorf("la session oubliée est encore là : %v", err)
	}
	if _, err := session.Charger(serveurB); err != nil {
		t.Errorf("oublier un serveur a emporté l'autre : %v", err)
	}
}

// `warden logout` est souvent lancé deux fois, et la seconde ne doit pas
// ressembler à une panne.
func TestOublierEstIdempotent(t *testing.T) {
	isole(t)

	if err := session.Oublier(serveurA); err != nil {
		t.Errorf("oublier sans fichier : %v", err)
	}
	_ = session.Ranger(serveurA, session1("alice"))
	if err := session.Oublier(serveurA); err != nil {
		t.Errorf("premier oubli : %v", err)
	}
	if err := session.Oublier(serveurA); err != nil {
		t.Errorf("second oubli : %v", err)
	}
}

// L'écriture est atomique : une écriture en place interrompue laisserait un JSON
// tronqué, donc TOUTES les sessions perdues d'un coup — y compris celles des
// serveurs qu'on ne touchait pas.
func TestLEcritureNeLaissePasDeTemporaire(t *testing.T) {
	dir := isole(t)

	for range 3 {
		if err := session.Ranger(serveurA, session1("alice")); err != nil {
			t.Fatalf("Ranger: %v", err)
		}
	}

	entrees, err := os.ReadDir(filepath.Join(dir, "warden"))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entrees {
		if strings.HasPrefix(e.Name(), ".credentials-") {
			t.Errorf("un temporaire est resté : %s", e.Name())
		}
	}
	if len(entrees) != 1 {
		t.Errorf("%d entrées dans le répertoire, attendu 1", len(entrees))
	}
}

// Un fichier corrompu ne doit pas BLOQUER : se reconnecter est précisément ce
// qui le répare, et refuser d'écrire enfermerait l'utilisateur dehors.
func TestUnFichierCorrompuNEmpechePasDeSeReconnecter(t *testing.T) {
	isole(t)

	chemin, _ := session.Chemin()
	if err := os.MkdirAll(filepath.Dir(chemin), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(chemin, []byte("{ceci n'est pas du JSON"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, err := session.Charger(serveurA); err == nil {
		t.Error("un fichier corrompu s'est lu sans erreur")
	}
	if err := session.Ranger(serveurA, session1("alice")); err != nil {
		t.Fatalf("Ranger sur un fichier corrompu : %v", err)
	}
	s, err := session.Charger(serveurA)
	if err != nil || s.Token != "un-jeton" {
		t.Errorf("la reconnexion n'a pas réparé le fichier : %v / %+v", err, s)
	}
}

// L'expiration est anticipée d'une minute : une session qui expire PENDANT un
// run ferait échouer une soumission à mi-chemin, sur un message parlant
// d'authentification alors que le vrai sujet est le calendrier.
func TestUneSessionSurLePointDExpirerEstDejaExpiree(t *testing.T) {
	t.Parallel()

	cas := map[string]struct {
		expire  time.Time
		expiree bool
	}{
		"largement valide": {time.Now().Add(time.Hour), false},
		"expirée":          {time.Now().Add(-time.Hour), true},
		"dans trente sec":  {time.Now().Add(30 * time.Second), true},
		"sans échéance":    {time.Time{}, false},
	}
	for nom, c := range cas {
		t.Run(nom, func(t *testing.T) {
			t.Parallel()
			s := session.Session{ExpiresAt: c.expire}
			if s.Expiree() != c.expiree {
				t.Errorf("Expiree() = %v, attendu %v", s.Expiree(), c.expiree)
			}
		})
	}
}

// Le jeton d'un autre serveur ne doit pas se retrouver dans le fichier sous une
// clé qu'on n'a pas voulue : la forme sur disque est vérifiée directement, parce
// qu'elle est un contrat avec les versions futures de la CLI.
func TestLaFormeSurDisqueEstIndexeeParServeur(t *testing.T) {
	isole(t)

	_ = session.Ranger(serveurA, session1("alice"))

	chemin, _ := session.Chemin()
	brut, err := os.ReadFile(chemin)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	var f struct {
		Servers map[string]json.RawMessage `json:"servers"`
	}
	if err := json.Unmarshal(brut, &f); err != nil {
		t.Fatalf("le fichier n'a pas la forme attendue : %v", err)
	}
	if _, ok := f.Servers[serveurA]; !ok {
		t.Errorf("clés = %v, attendu %q", f.Servers, serveurA)
	}
}

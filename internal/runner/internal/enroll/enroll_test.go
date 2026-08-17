// Étage 1 de l'ADR-0010 : un VRAI serveur SSH, en mémoire, aucun conteneur.
//
// `x/crypto/ssh` en sert un ; ce paquet en monte donc un qui rejoue ce qu'une
// machine fait — poignée de main, authentification par mot de passe, exécution
// d'une commande dans un `authorized_keys` simulé. C'est le seul montage qui
// vérifie ce qui compte ici : que l'épinglage refuse une machine substituée, et
// que la pose de la clé est idempotente.

package enroll_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/jijins1/warden/internal/runner/internal/enroll"
)

const (
	compte     = "ansible"
	motDePasse = "hunter2-tres-secret"
)

// machine est un serveur SSH de test : il tient un `authorized_keys` en mémoire
// et exécute le script de pose comme le ferait un shell POSIX.
type machine struct {
	t          *testing.T
	listener   net.Listener
	signataire ssh.Signer

	mu        sync.Mutex
	autorise  []string // le contenu d'authorized_keys
	refuseMDP bool
	sessions  int
}

func demarrer(t *testing.T) *machine {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("clé d'hôte : %v", err)
	}
	sig, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("signataire : %v", err)
	}

	var lc net.ListenConfig
	l, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("écoute : %v", err)
	}

	m := &machine{t: t, listener: l, signataire: sig}
	t.Cleanup(func() { _ = l.Close() })
	go m.servir()
	return m
}

func (m *machine) adresse() (string, int) {
	h, p, _ := net.SplitHostPort(m.listener.Addr().String())
	n, _ := strconv.Atoi(p)
	return h, n
}

// empreinte rend la clé d'hôte au format `authorized_keys`.
func (m *machine) empreinte() string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(m.signataire.PublicKey())))
}

func (m *machine) contenu() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.autorise...)
}

func (m *machine) servir() {
	for {
		conn, err := m.listener.Accept()
		if err != nil {
			return
		}
		go m.session(conn)
	}
}

func (m *machine) session(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, mdp []byte) (*ssh.Permissions, error) {
			m.mu.Lock()
			refuse := m.refuseMDP
			m.mu.Unlock()
			if refuse || c.User() != compte || string(mdp) != motDePasse {
				return nil, fmt.Errorf("refusé")
			}
			return &ssh.Permissions{}, nil
		},
		// L'authentification par clé publique accepte ce qui est dans
		// l'authorized_keys, ce qui est exactement ce que la vérification finale
		// interroge.
		PublicKeyCallback: func(_ ssh.ConnMetadata, cle ssh.PublicKey) (*ssh.Permissions, error) {
			m.mu.Lock()
			defer m.mu.Unlock()
			for _, l := range m.autorise {
				pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(l))
				if err == nil && string(pub.Marshal()) == string(cle.Marshal()) {
					return &ssh.Permissions{}, nil
				}
			}
			return nil, fmt.Errorf("clé inconnue")
		},
	}
	cfg.AddHostKey(m.signataire)

	sc, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	defer func() { _ = sc.Close() }()
	go ssh.DiscardRequests(reqs)

	for nouveau := range chans {
		if nouveau.ChannelType() != "session" {
			_ = nouveau.Reject(ssh.UnknownChannelType, "inconnu")
			continue
		}
		canal, requetes, err := nouveau.Accept()
		if err != nil {
			return
		}
		m.executer(canal, requetes)
	}
}

// executer rejoue le script de pose : il lit la clé sur l'entrée standard et
// l'ajoute si elle n'y est pas.
func (m *machine) executer(canal ssh.Channel, requetes <-chan *ssh.Request) {
	defer func() { _ = canal.Close() }()

	for req := range requetes {
		if req.Type != "exec" {
			_ = req.Reply(false, nil)
			continue
		}
		_ = req.Reply(true, nil)

		m.mu.Lock()
		m.sessions++
		m.mu.Unlock()

		// La clé arrive sur stdin. Lire JUSQU'À EOF et non une fois : un seul
		// `Read` peut revenir vide avant que le client ait écrit, et le faux
		// serveur conclurait « clé absente » sur une course.
		brut, _ := io.ReadAll(canal)
		cle := strings.TrimSpace(string(brut))

		m.mu.Lock()
		deja := false
		for _, l := range m.autorise {
			if l == cle {
				deja = true
				break
			}
		}
		if !deja && cle != "" {
			m.autorise = append(m.autorise, cle)
		}
		m.mu.Unlock()

		// Le CODE DE SORTIE porte la réponse, comme le vrai script. La sortie
		// standard ne portait pas ce signal de façon fiable : elle se perdait
		// au gré de la fermeture du canal.
		code := uint32(0)
		if deja {
			code = enroll.CodeDejaPresente
		}
		_ = canal.CloseWrite()
		_, _ = canal.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{code}))
		return
	}
}

// clePublique frappe une paire et rend la ligne `authorized_keys`.
func clePublique(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("frappe : %v", err)
	}
	s, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("clé publique : %v", err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(s))) + " warden-prod"
}

func cible(t *testing.T, m *machine) enroll.Cible {
	t.Helper()
	adr, port := m.adresse()
	return enroll.Cible{
		Host: "db-prod-02", Address: adr, Port: port,
		Login: compte, Password: []byte(motDePasse),
		PublicKey: clePublique(t),
	}
}

// --- le chemin nominal ---

func TestUnEnrolementPoseLaCleEtLaVerifie(t *testing.T) {
	m := demarrer(t)
	c := cible(t, m)

	var etapes []string
	res, err := enroll.Enroler(t.Context(), c, func(e string) { etapes = append(etapes, e) })
	if err != nil {
		t.Fatalf("Enroler: %v", err)
	}

	if len(m.contenu()) != 1 || m.contenu()[0] != c.PublicKey {
		t.Errorf("authorized_keys = %q", m.contenu())
	}
	// L'empreinte CONSTATÉE est rendue : c'est elle qu'on épinglera ensuite, et
	// sans elle le premier contact ne servirait à rien.
	if res.HostKey != m.empreinte() {
		t.Errorf("empreinte rendue = %q, attendu %q", res.HostKey, m.empreinte())
	}
	if !strings.HasPrefix(res.Fingerprint, "SHA256:") {
		t.Errorf("empreinte lisible = %q", res.Fingerprint)
	}
	if res.DejaPresente {
		t.Error("la clé est signalée déjà présente sur une machine vierge")
	}

	// Le récit devient de l'audit : il doit dire ce qui a eu lieu, empreinte
	// comprise.
	recit := strings.Join(etapes, "\n")
	for _, attendu := range []string{"connexion", "empreinte", "authorized_keys", "vérifié"} {
		if !strings.Contains(recit, attendu) {
			t.Errorf("le récit ne mentionne pas %q :\n%s", attendu, recit)
		}
	}
}

// Réenrôler ne DOUBLE pas la ligne. Un `authorized_keys` qui grossit à chaque
// passage finit par être illisible, et personne n'ose plus y toucher.
func TestUnSecondEnrolementNeDoublePasLaLigne(t *testing.T) {
	m := demarrer(t)
	c := cible(t, m)

	if _, err := enroll.Enroler(t.Context(), c, nil); err != nil {
		t.Fatalf("premier enrôlement : %v", err)
	}
	res, err := enroll.Enroler(t.Context(), c, nil)
	if err != nil {
		t.Fatalf("second enrôlement : %v", err)
	}

	if len(m.contenu()) != 1 {
		t.Errorf("authorized_keys contient %d lignes, attendu 1", len(m.contenu()))
	}
	if !res.DejaPresente {
		t.Error("le second enrôlement n'a pas signalé que la clé y était déjà")
	}
}

// --- l'épinglage, LE point de sécurité ---

// Une empreinte déclarée qui ne correspond pas fait ÉCHOUER, et sur une erreur
// DISTINCTE : la conduite à tenir est l'inverse d'une panne réseau — on ne
// réessaie pas, on enquête.
func TestUneEmpreinteQuiNeCorrespondPasFaitEchouer(t *testing.T) {
	m := demarrer(t)
	autre := demarrer(t) // une autre machine, donc une autre clé d'hôte

	c := cible(t, m)
	c.HostKey = autre.empreinte()

	_, err := enroll.Enroler(t.Context(), c, nil)
	if !errors.Is(err, enroll.ErrEmpreinteChangee) {
		t.Fatalf("erreur = %v, attendu ErrEmpreinteChangee", err)
	}
	// Et RIEN n'a été posé : on ne laisse pas sa clé chez un inconnu.
	if len(m.contenu()) != 0 {
		t.Errorf("une clé a été posée malgré l'écart d'empreinte : %q", m.contenu())
	}
}

// L'empreinte attendue est comparée sur les OCTETS de la clé, pas sur le texte :
// une même clé peut s'écrire avec un commentaire différent, et comparer les
// chaînes ferait échouer un enrôlement parfaitement légitime.
func TestUnCommentaireDifferentNeFaitPasEchouerLEpinglage(t *testing.T) {
	m := demarrer(t)

	c := cible(t, m)
	c.HostKey = m.empreinte() + " un-commentaire-qui-vient-d-ailleurs"

	if _, err := enroll.Enroler(t.Context(), c, nil); err != nil {
		t.Fatalf("Enroler: %v", err)
	}
}

// Sans empreinte attendue, la connexion accepte ce qui répond et le CAPTURE.
// C'est la confiance à la première rencontre, et elle n'est pas protégée.
func TestSansEmpreinteAttendueLaPremiereRencontreEstAcceptee(t *testing.T) {
	m := demarrer(t)

	res, err := enroll.Enroler(t.Context(), cible(t, m), nil)
	if err != nil {
		t.Fatalf("Enroler: %v", err)
	}
	if res.HostKey != m.empreinte() {
		t.Errorf("l'empreinte n'a pas été capturée : %q", res.HostKey)
	}
}

// --- refus ---

func TestUnMotDePasseRefuseLeDitClairement(t *testing.T) {
	m := demarrer(t)
	m.mu.Lock()
	m.refuseMDP = true
	m.mu.Unlock()

	_, err := enroll.Enroler(t.Context(), cible(t, m), nil)
	if !errors.Is(err, enroll.ErrAuthentification) {
		t.Fatalf("erreur = %v, attendu ErrAuthentification", err)
	}
}

func TestUneMachineInjoignableLeDit(t *testing.T) {
	c := enroll.Cible{
		Host: "fantome", Address: "127.0.0.1", Port: 1,
		Login: compte, Password: []byte(motDePasse),
		PublicKey: clePublique(t),
	}
	if _, err := enroll.Enroler(t.Context(), c, nil); err == nil {
		t.Fatal("une machine injoignable a été enrôlée")
	}
}

func TestUneCibleIncompleteEstRefuseeAvantToutReseau(t *testing.T) {
	valide := enroll.Cible{
		Address: "127.0.0.1", Port: 22, Login: compte,
		Password: []byte(motDePasse), PublicKey: clePublique(t),
	}

	cas := map[string]func(enroll.Cible) enroll.Cible{
		"sans adresse":       func(c enroll.Cible) enroll.Cible { c.Address = ""; return c },
		"sans compte":        func(c enroll.Cible) enroll.Cible { c.Login = ""; return c },
		"sans mot de passe":  func(c enroll.Cible) enroll.Cible { c.Password = nil; return c },
		"sans clé publique":  func(c enroll.Cible) enroll.Cible { c.PublicKey = ""; return c },
		"clé publique bidon": func(c enroll.Cible) enroll.Cible { c.PublicKey = "pas une clé"; return c },
		"port hors bornes":   func(c enroll.Cible) enroll.Cible { c.Port = 0; return c },
	}
	for nom, casser := range cas {
		t.Run(nom, func(t *testing.T) {
			if _, err := enroll.Enroler(t.Context(), casser(valide), nil); err == nil {
				t.Fatal("accepté à tort")
			}
		})
	}
}

// L'annulation du §5.6 doit rendre la main : un pod qui tient un mot de passe ne
// doit pas attendre le délai complet parce que quelqu'un a fait Ctrl-C.
func TestUneAnnulationRendLaMain(t *testing.T) {
	// Un port qui accepte au niveau TCP — le noyau le fait via le backlog — mais
	// où personne ne parle SSH. C'est le cas qui pend, et celui que l'annulation
	// doit interrompre.
	var lc net.ListenConfig
	l, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("écoute : %v", err)
	}
	defer func() { _ = l.Close() }()
	h, p, _ := net.SplitHostPort(l.Addr().String())
	port, _ := strconv.Atoi(p)

	ctx, annuler := context.WithCancel(t.Context())
	go func() {
		time.Sleep(100 * time.Millisecond)
		annuler()
	}()

	fini := make(chan error, 1)
	go func() {
		_, err := enroll.Enroler(ctx, enroll.Cible{
			Host: "muette", Address: h, Port: port,
			Login: compte, Password: []byte(motDePasse),
			PublicKey: clePublique(t),
		}, nil)
		fini <- err
	}()

	select {
	case err := <-fini:
		if err == nil {
			t.Error("un enrôlement annulé a réussi")
		}
	case <-time.After(enroll.DelaiConnexion):
		t.Fatal("l'enrôlement n'a pas rendu la main après annulation")
	}
}

// --- masquage ---

// Le mot de passe traverse une structure qui finira dans une trace. Il ne doit
// s'y trouver sous aucun des trois verbes d'impression.
// Ce paquet reçoit un mot de passe DÉMASQUÉ — le masquage est porté par le
// contrat de fil, en amont. Ce qu'il doit garantir, lui, c'est que rien de ce
// qu'il ÉCRIT ne le contient : le récit part dans l'audit, qui ne se purge
// jamais (ADR-0013).
func TestLeMotDePasseNAppartientPasAuRecitDAudit(t *testing.T) {
	m := demarrer(t)
	c := cible(t, m)
	var etapes []string
	if _, err := enroll.Enroler(t.Context(), c, func(e string) { etapes = append(etapes, e) }); err != nil {
		t.Fatalf("Enroler: %v", err)
	}
	if strings.Contains(strings.Join(etapes, "\n"), motDePasse) {
		t.Error("le mot de passe apparaît dans le récit d'audit")
	}
}

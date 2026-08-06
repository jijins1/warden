// Étage 1 de l'ADR-0010 : ni base, ni conteneur, ni cluster.
//
// Ce paquet est testable sans PostgreSQL parce qu'il ne le connaît pas : les
// deux ports — résolution des pods, construction de l'ordre — sont déclarés là
// où ils sont utilisés et doublés ici. C'est la propriété que la décomposition
// hexagonale de l'ADR-0019 achète, et ce fichier est l'endroit où elle se paie.
//
// Le test central est NÉGATIF, et l'ADR-0018 l'exige nommément pour ce
// troisième port : « un pod chaud ne doit rien pouvoir obtenir d'un run qui ne
// lui est pas attribué ».
package dispatch_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/jijins1/warden/internal/server/internal/core"
	"github.com/jijins1/warden/internal/server/internal/outbound/dispatch"
	wire "github.com/jijins1/warden/internal/wire/dispatch"
)

// pollInterval du paquet testé. Dupliqué parce qu'il n'est pas exporté ; les
// bornes de temps d'ici s'y réfèrent.
const pollInterval = 2 * time.Second

var errPodInconnu = errors.New("pod inconnu")

// podsFake double le port de résolution des pods.
type podsFake struct {
	mu       sync.Mutex
	parJeton map[string]int64
	etats    map[int64]core.PodState
	ready    map[int64]bool
	// appelsReady compte les transitions demandées, pour vérifier le comptage
	// de références quand un pod ouvre deux canaux.
	appelsReady []bool
	erreurEtat  error
}

func nouveauxPods() *podsFake {
	return &podsFake{
		parJeton: make(map[string]int64),
		etats:    make(map[int64]core.PodState),
		ready:    make(map[int64]bool),
	}
}

func (p *podsFake) enregistrer(podID int64) core.PodToken {
	tok, err := core.NewPodToken()
	if err != nil {
		panic(err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.parJeton[string(tok.Hash())] = podID
	p.etats[podID] = core.PodState{PodID: podID}
	return tok
}

func (p *podsFake) muter(podID int64, f func(*core.PodState)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	st := p.etats[podID]
	f(&st)
	p.etats[podID] = st
}

func (p *podsFake) estPret(podID int64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ready[podID]
}

func (p *podsFake) transitions() []bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]bool(nil), p.appelsReady...)
}

func (p *podsFake) AuthenticatePod(_ context.Context, token core.PodToken) (core.PodIdentity, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	podID, ok := p.parJeton[string(token.Hash())]
	if !ok {
		return core.PodIdentity{}, errPodInconnu
	}
	return core.PodIdentity{PodID: podID, RunID: p.etats[podID].RunID}, nil
}

func (p *podsFake) SetPodReady(_ context.Context, podID int64, ready bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ready[podID] = ready
	p.appelsReady = append(p.appelsReady, ready)
	return nil
}

func (p *podsFake) PodStates(_ context.Context, podIDs []int64) ([]core.PodState, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.erreurEtat != nil {
		return nil, p.erreurEtat
	}
	out := make([]core.PodState, 0, len(podIDs))
	for _, id := range podIDs {
		if st, ok := p.etats[id]; ok {
			out = append(out, st)
		}
	}
	return out, nil
}

// ordersFake double le port de construction de l'ordre, et compte les appels :
// c'est ainsi qu'on vérifie qu'AUCUN credential n'est assemblé avant le claim
// (§8.3, « aucun credential avant le claim »).
type ordersFake struct {
	mu     sync.Mutex
	appels int
	err    error
}

func (o *ordersFake) OrderFor(_ context.Context, runID int64) (wire.Order, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.appels++
	if o.err != nil {
		return wire.Order{}, o.err
	}
	return wire.Order{
		RunRef:   "01JZQ00000000000000000ABCD",
		AuditSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Playbook: "site.yml",
		GitToken: wire.Secret("ght_secret"),
		SSHKey:   wire.Secret("-----BEGIN KEY-----\nxx\n-----END KEY-----\n"),
	}, nil
}

func (o *ordersFake) nombreDAppels() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.appels
}

// canal ouvre une connexion de dispatch et rend un lecteur de messages.
type canal struct {
	t    *testing.T
	resp *http.Response
	dec  *wire.Decoder
	recu chan wire.Message
	err  chan error
}

func ouvrir(t *testing.T, srv *httptest.Server, token string) (*canal, int) {
	t.Helper()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("requête: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("connexion: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		code := resp.StatusCode
		_ = resp.Body.Close()
		return nil, code
	}

	c := &canal{
		t:    t,
		resp: resp,
		dec:  wire.NewDecoder(resp.Body),
		recu: make(chan wire.Message, 8),
		err:  make(chan error, 1),
	}
	go func() {
		for {
			m, err := c.dec.Next()
			if err != nil {
				c.err <- err
				return
			}
			c.recu <- m
		}
	}()
	t.Cleanup(c.fermer)
	return c, http.StatusOK
}

func (c *canal) fermer() { _ = c.resp.Body.Close() }

// suivant attend un message, ou échoue au bout du délai.
func (c *canal) suivant(delai time.Duration, quoi string) wire.Message {
	c.t.Helper()
	select {
	case m := <-c.recu:
		return m
	case err := <-c.err:
		c.t.Fatalf("canal rompu en attendant %s: %v", quoi, err)
	case <-time.After(delai):
		c.t.Fatalf("aucun message en %s en attendant %s", delai, quoi)
	}
	return wire.Message{}
}

// rien vérifie qu'aucun message n'arrive pendant le délai.
func (c *canal) rien(delai time.Duration) (wire.Message, bool) {
	c.t.Helper()
	select {
	case m := <-c.recu:
		return m, false
	case <-time.After(delai):
		return wire.Message{}, true
	}
}

func monter(t *testing.T) (*dispatch.Dispatcher, *podsFake, *ordersFake, *httptest.Server) {
	t.Helper()
	pods := nouveauxPods()
	orders := &ordersFake{}
	d := dispatch.New(pods, orders)
	srv := httptest.NewServer(d)
	t.Cleanup(srv.Close)
	return d, pods, orders, srv
}

// Le test négatif que l'ADR-0018 exige pour ce troisième port : un pod chaud
// connecté et non réclamé n'obtient RIEN. Et, ce qui va plus loin, aucun
// credential n'est même assemblé pour lui (§8.3).
func TestPodNonReclameNObtientRien(t *testing.T) {
	_, pods, orders, srv := monter(t)
	tok := pods.enregistrer(42)

	c, code := ouvrir(t, srv, tok.Reveal())
	if code != http.StatusOK {
		t.Fatalf("code = %d", code)
	}

	// Le seul message dû à un pod oisif : la confirmation d'ouverture.
	if m := c.suivant(2*time.Second, "l'ouverture"); m.Kind != wire.KindHello {
		t.Fatalf("premier message de type %q, attendu %q", m.Kind, wire.KindHello)
	}
	if m, silence := c.rien(pollInterval + 500*time.Millisecond); !silence {
		t.Errorf("un pod non réclamé a reçu un message %q: %+v", m.Kind, m)
	}
	if n := orders.nombreDAppels(); n != 0 {
		t.Errorf("%d ordres assemblés pour un pod non réclamé — aucun credential ne doit être construit avant le claim (§8.3)", n)
	}
}

// Le chemin nominal, et la latence qui va avec : la notification, pas le poll.
func TestOrdreLivreALaNotification(t *testing.T) {
	d, pods, _, srv := monter(t)
	tok := pods.enregistrer(42)

	c, _ := ouvrir(t, srv, tok.Reveal())
	c.suivant(2*time.Second, "l'ouverture")

	pods.muter(42, func(st *core.PodState) { st.RunID = 7 })
	debut := time.Now()
	d.Wake(42)

	m := c.suivant(2*time.Second, "l'ordre")
	ecoule := time.Since(debut)

	if m.Kind != wire.KindOrder {
		t.Fatalf("message de type %q, attendu %q", m.Kind, wire.KindOrder)
	}
	if m.Order == nil || m.Order.RunRef != "01JZQ00000000000000000ABCD" {
		t.Fatalf("ordre inattendu: %+v", m.Order)
	}
	// La clé et le jeton traversent le canal : c'est leur seul chemin d'arrivée
	// dans le pod (ADR-0018).
	if len(m.Order.SSHKey.Reveal()) == 0 || len(m.Order.GitToken.Reveal()) == 0 {
		t.Error("l'ordre est arrivé sans les credentials")
	}
	// Discriminant : le poll aurait mis jusqu'à pollInterval. Si cette borne
	// saute, c'est que la notification ne sert plus à rien.
	if ecoule > pollInterval/2 {
		t.Errorf("ordre livré en %s : la notification ne joue pas, c'est le poll qui a servi", ecoule)
	}
}

// NOTIFY est une OPTIMISATION, pas le mécanisme de correction. Sans aucune
// notification, le pod reçoit quand même son ordre — plus lentement. C'est ce
// qui rend une panne du routage bénigne.
func TestOrdreLivreParLePollSansNotification(t *testing.T) {
	_, pods, _, srv := monter(t)
	tok := pods.enregistrer(42)

	c, _ := ouvrir(t, srv, tok.Reveal())
	c.suivant(2*time.Second, "l'ouverture")

	// Aucun appel à Wake, délibérément.
	pods.muter(42, func(st *core.PodState) { st.RunID = 7 })

	if m := c.suivant(3*pollInterval, "l'ordre servi par le poll"); m.Kind != wire.KindOrder {
		t.Fatalf("message de type %q, attendu %q", m.Kind, wire.KindOrder)
	}
}

// Un ordre est livré UNE fois. Le pod n'étant jamais réutilisé (§8.2), un
// second ordre n'aurait aucun sens — et le réveil est répété par nature, chaque
// notification déclenchant une relecture complète de l'état.
func TestOrdreEtAnnulationLivresUneSeuleFois(t *testing.T) {
	d, pods, orders, srv := monter(t)
	tok := pods.enregistrer(42)

	c, _ := ouvrir(t, srv, tok.Reveal())
	c.suivant(2*time.Second, "l'ouverture")

	pods.muter(42, func(st *core.PodState) { st.RunID = 7 })
	d.Wake(42)
	if m := c.suivant(2*time.Second, "l'ordre"); m.Kind != wire.KindOrder {
		t.Fatalf("message de type %q", m.Kind)
	}

	for i := 0; i < 3; i++ {
		d.Wake(42)
	}
	if m, silence := c.rien(pollInterval + 500*time.Millisecond); !silence {
		t.Errorf("l'ordre a été livré deux fois (%q)", m.Kind)
	}
	if n := orders.nombreDAppels(); n != 1 {
		t.Errorf("%d assemblages d'ordre, attendu 1", n)
	}

	// L'annulation du §5.6, elle aussi livrée une fois.
	pods.muter(42, func(st *core.PodState) { st.CancelRequestedAt = time.Now() })
	d.Wake(42)
	if m := c.suivant(2*time.Second, "l'annulation"); m.Kind != wire.KindCancel {
		t.Fatalf("message de type %q, attendu %q", m.Kind, wire.KindCancel)
	}
	d.Wake(42)
	if m, silence := c.rien(pollInterval + 500*time.Millisecond); !silence {
		t.Errorf("l'annulation a été livrée deux fois (%q)", m.Kind)
	}
}

// Un pod libéré est définitivement hors service (§8.2). Le canal se ferme.
func TestPodLibereFermeLeCanal(t *testing.T) {
	d, pods, _, srv := monter(t)
	tok := pods.enregistrer(42)

	c, _ := ouvrir(t, srv, tok.Reveal())
	c.suivant(2*time.Second, "l'ouverture")

	pods.muter(42, func(st *core.PodState) { st.ReleasedAt = time.Now() })
	d.Wake(42)

	select {
	case err := <-c.err:
		if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
			// Une rupture de transport est acceptable : ce qui compte est que
			// le canal se ferme.
			t.Logf("canal fermé sur %v", err)
		}
	case m := <-c.recu:
		t.Fatalf("un pod libéré a reçu un message %q", m.Kind)
	case <-time.After(2 * pollInterval):
		t.Error("le canal d'un pod libéré est resté ouvert")
	}
}

// La disponibilité suit la connexion, avec un comptage : deux canaux ouverts
// par le même pod ne doivent pas se marcher dessus. Sans le comptage, la
// fermeture du premier rendrait le pod injoignable alors que le second tient
// toujours — et il ne serait plus jamais réclamé.
func TestDisponibiliteComptee(t *testing.T) {
	d, pods, _, srv := monter(t)
	tok := pods.enregistrer(42)

	premier, _ := ouvrir(t, srv, tok.Reveal())
	premier.suivant(2*time.Second, "l'ouverture")
	if !pods.estPret(42) {
		t.Fatal("le pod n'est pas marqué joignable après l'ouverture")
	}
	if d.Connected() != 1 {
		t.Errorf("Connected = %d, attendu 1", d.Connected())
	}

	second, _ := ouvrir(t, srv, tok.Reveal())
	second.suivant(2*time.Second, "l'ouverture")

	premier.fermer()
	// Laisser le handler se démonter.
	time.Sleep(300 * time.Millisecond)
	if !pods.estPret(42) {
		t.Error("le pod a été marqué injoignable alors qu'un canal reste ouvert")
	}

	second.fermer()
	attendre(t, 2*time.Second, func() bool { return !pods.estPret(42) })

	// Une seule transition dans chaque sens : le comptage a fait son office.
	if trans := pods.transitions(); len(trans) != 2 || !trans[0] || trans[1] {
		t.Errorf("transitions de disponibilité = %v, attendu [true false]", trans)
	}
	if d.Connected() != 0 {
		t.Errorf("Connected = %d après fermeture, attendu 0", d.Connected())
	}
}

// L'ADR-0018 fait de ce port une surface joignable depuis du code arbitraire.
// Un jeton absent, malformé ou inconnu ne donne rien — et surtout la même
// chose : distinguer les cas dirait à un porteur si un pod a existé.
func TestJetonRefuseSansRienApprendre(t *testing.T) {
	_, pods, _, srv := monter(t)
	pods.enregistrer(42)

	inconnu, err := core.NewPodToken()
	if err != nil {
		t.Fatalf("NewPodToken: %v", err)
	}

	refuses := map[string]string{
		"absent":            "",
		"malformé":          "pas-un-jeton",
		"vide après Bearer": " ",
		"inconnu":           inconnu.Reveal(),
	}
	for nom, jeton := range refuses {
		if _, code := ouvrir(t, srv, jeton); code != http.StatusUnauthorized {
			t.Errorf("jeton %s: code %d, attendu 401", nom, code)
		}
	}
	if trans := pods.transitions(); len(trans) != 0 {
		t.Errorf("un jeton refusé a touché à la disponibilité: %v", trans)
	}
}

// Une panne de la base ne doit pas coûter le canal : le runner ne rouvre PAS de
// canal en cours de vie (ADR-0018), donc fermer serait définitif.
func TestPanneDeLectureNeFermePasLeCanal(t *testing.T) {
	d, pods, _, srv := monter(t)
	tok := pods.enregistrer(42)

	c, _ := ouvrir(t, srv, tok.Reveal())
	c.suivant(2*time.Second, "l'ouverture")

	pods.mu.Lock()
	pods.erreurEtat = errors.New("base indisponible")
	pods.mu.Unlock()

	if _, silence := c.rien(pollInterval + 500*time.Millisecond); !silence {
		t.Error("un message est arrivé pendant la panne")
	}

	// La panne passe, le claim survient : l'ordre est livré comme si de rien
	// n'était.
	pods.mu.Lock()
	pods.erreurEtat = nil
	pods.mu.Unlock()
	pods.muter(42, func(st *core.PodState) { st.RunID = 7 })
	d.Wake(42)

	if m := c.suivant(3*pollInterval, "l'ordre après la panne"); m.Kind != wire.KindOrder {
		t.Fatalf("message de type %q, attendu %q", m.Kind, wire.KindOrder)
	}
}

func attendre(t *testing.T, delai time.Duration, cond func() bool) {
	t.Helper()
	fin := time.Now().Add(delai)
	for time.Now().Before(fin) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("condition non atteinte en %s", delai)
}

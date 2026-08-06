//go:build integration

// Étage 2 de l'ADR-0010, pour l'identité d'amorçage et le claim (ADR-0023).
//
// Trois propriétés valent d'être établies contre une vraie base plutôt que
// raisonnées : que le claim n'attribue jamais deux fois le même pod ET ne fasse
// jamais attendre (§8.4), que la clé de pool sépare effectivement les
// utilisateurs, et que le jeton d'amorçage ne permette rien de plus que ce que
// l'ADR-0023 lui accorde.
package store_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jijins1/warden/internal/server/internal/core"
	"github.com/jijins1/warden/internal/server/internal/outbound/store"
)

const testULIDBis = "01JZQ00000000000000000ABCE"

func seedUser(t *testing.T, s *store.Store, subject string) int64 {
	t.Helper()
	u, err := s.UpsertUser(context.Background(), store.Identity{
		Type: core.UserLocal, Subject: subject, DisplayName: subject,
	})
	if err != nil {
		t.Fatalf("UpsertUser(%s): %v", subject, err)
	}
	return u.ID
}

func seedRunFor(t *testing.T, s *store.Store, userID int64, ref string) int64 {
	t.Helper()
	id, err := s.CreateRun(context.Background(), store.NewRun{
		RunRef: ref, UserID: userID,
		Environment: "staging", Inventory: "inv/staging", Playbook: "site.yml",
		AuditSHA:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		BaseSHA:     "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		MainlineRef: "refs/heads/main",
		MainlineSHA: "cccccccccccccccccccccccccccccccccccccccc",
	})
	if err != nil {
		t.Fatalf("CreateRun(%s): %v", ref, err)
	}
	return id
}

// seedPod enregistre un pod chaud et le marque joignable, comme le ferait le
// contrôleur suivi de l'ouverture du canal de dispatch.
func seedPod(t *testing.T, s *store.Store, name string, pool core.PodPool, ready bool) (int64, core.PodToken) {
	t.Helper()
	ctx := context.Background()

	tok, err := core.NewPodToken()
	if err != nil {
		t.Fatalf("NewPodToken: %v", err)
	}
	id, err := s.RegisterPod(ctx, store.NewPod{PodName: name, Pool: pool, Token: tok})
	if err != nil {
		t.Fatalf("RegisterPod(%s): %v", name, err)
	}
	if ready {
		if err := s.SetPodReady(ctx, id, true); err != nil {
			t.Fatalf("SetPodReady(%s): %v", name, err)
		}
	}
	return id, tok
}

// Le serveur ne détient que l'empreinte (ADR-0023) : compromettre la base ne
// donne aucune identité de pod. C'est ce qui rend acceptable de garder cet état
// dans la base d'audit.
func TestJetonDAmorcageNestJamaisStockeEnClair(t *testing.T) {
	dsn := startPostgres(t)
	ctx := context.Background()

	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	uid := seedUser(t, s, "alice")
	pool := core.PodPool{UserID: uid, Repo: "infra", Environment: "staging"}
	podID, tok := seedPod(t, s, "warden-runner-aaaa", pool, true)

	c := connect(t, dsn)
	defer c.Close(ctx)

	var stored []byte
	if err := c.QueryRow(ctx, `SELECT token_hash FROM runner_pod WHERE id = $1`, podID).Scan(&stored); err != nil {
		t.Fatalf("lecture de l'empreinte: %v", err)
	}
	if string(stored) == tok.Reveal() {
		t.Fatal("le jeton est stocké en clair")
	}
	if string(stored) != string(tok.Hash()) {
		t.Errorf("l'empreinte stockée ne correspond pas au SHA-256 du jeton")
	}

	// La ligne entière ne doit contenir le jeton nulle part — pas seulement la
	// colonne prévue pour lui.
	var ligne string
	if err := c.QueryRow(ctx, `SELECT runner_pod::text FROM runner_pod WHERE id = $1`, podID).Scan(&ligne); err != nil {
		t.Fatalf("lecture de la ligne: %v", err)
	}
	if strings.Contains(ligne, tok.Reveal()) {
		t.Error("le jeton apparaît dans la ligne de runner_pod")
	}
}

// Le §8.4 : deux commandes concurrentes ne doivent ni recevoir le même pod, ni
// attendre. Ici le premier volet — un pod attribué ne l'est plus jamais, et le
// second claim tombe en cold start.
func TestClaimNattribuePasDeuxFoisLeMemePod(t *testing.T) {
	dsn := startPostgres(t)
	ctx := context.Background()

	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	uid := seedUser(t, s, "alice")
	pool := core.PodPool{UserID: uid, Repo: "infra", Environment: "staging"}
	seedPod(t, s, "warden-runner-aaaa", pool, true)

	run1 := seedRunFor(t, s, uid, testULID)
	run2 := seedRunFor(t, s, uid, testULIDBis)

	c1, err := s.ClaimPod(ctx, pool, run1)
	if err != nil {
		t.Fatalf("premier claim: %v", err)
	}
	if c1.PodName != "warden-runner-aaaa" {
		t.Errorf("pod attribué = %q", c1.PodName)
	}

	if _, err := s.ClaimPod(ctx, pool, run2); !errors.Is(err, store.ErrNoWarmPod) {
		t.Fatalf("second claim = %v, attendu ErrNoWarmPod (cold start propre, §8.4)", err)
	}

	// Avec un second pod chaud, le second run est servi — et par l'autre pod.
	seedPod(t, s, "warden-runner-bbbb", pool, true)
	c2, err := s.ClaimPod(ctx, pool, run2)
	if err != nil {
		t.Fatalf("second claim après ajout d'un pod: %v", err)
	}
	if c2.PodID == c1.PodID {
		t.Error("le même pod a été attribué à deux runs")
	}
}

// Le second volet du §8.4, et le vrai motif du SKIP LOCKED : un claim
// concurrent ne doit pas ATTENDRE. Sans SKIP LOCKED, l'appel ci-dessous
// bloquerait jusqu'à la fin de la transaction qui tient le verrou.
func TestClaimNattendPasUnPodVerrouille(t *testing.T) {
	dsn := startPostgres(t)
	ctx := context.Background()

	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	uid := seedUser(t, s, "alice")
	pool := core.PodPool{UserID: uid, Repo: "infra", Environment: "staging"}
	podID, _ := seedPod(t, s, "warden-runner-aaaa", pool, true)
	runID := seedRunFor(t, s, uid, testULID)

	// Un autre réplica tient le verrou sur la seule ligne réclamable.
	tenant := connect(t, dsn)
	defer tenant.Close(ctx)
	tx, err := tenant.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT id FROM runner_pod WHERE id = $1 FOR UPDATE`, podID); err != nil {
		t.Fatalf("verrouillage: %v", err)
	}

	// Une limite de temps très supérieure au temps d'une requête, et très
	// inférieure à une attente de verrou : si le claim bloque, on le voit.
	borne, annule := context.WithTimeout(ctx, 3*time.Second)
	defer annule()

	debut := time.Now()
	_, err = s.ClaimPod(borne, pool, runID)
	ecoule := time.Since(debut)

	if !errors.Is(err, store.ErrNoWarmPod) {
		t.Fatalf("claim sur pod verrouillé = %v, attendu ErrNoWarmPod sans attente", err)
	}
	if ecoule > time.Second {
		t.Errorf("le claim a attendu %s : SKIP LOCKED ne joue pas", ecoule)
	}
}

// La clé de pool du §8.3 n'est pas qu'une optimisation de cache : c'est ce qui
// interdit qu'un pod chaud d'un utilisateur exécute le run d'un autre.
func TestClaimNeTraversePasLaCleDePool(t *testing.T) {
	dsn := startPostgres(t)
	ctx := context.Background()

	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	alice := seedUser(t, s, "alice")
	bob := seedUser(t, s, "bob")

	poolAlice := core.PodPool{UserID: alice, Repo: "infra", Environment: "staging"}
	seedPod(t, s, "warden-runner-alice", poolAlice, true)

	runBob := seedRunFor(t, s, bob, testULID)

	autres := []struct {
		nom  string
		pool core.PodPool
	}{
		{"un autre utilisateur", core.PodPool{UserID: bob, Repo: "infra", Environment: "staging"}},
		{"un autre dépôt", core.PodPool{UserID: alice, Repo: "apps", Environment: "staging"}},
		{"un autre environnement", core.PodPool{UserID: alice, Repo: "infra", Environment: "prod"}},
	}
	for _, c := range autres {
		if _, err := s.ClaimPod(ctx, c.pool, runBob); !errors.Is(err, store.ErrNoWarmPod) {
			t.Errorf("le pod d'alice a été attribué depuis %s: %v", c.nom, err)
		}
	}
}

// Un pod n'est réclamable que connecté (ADR-0018 : « choisir une connexion
// oisive ») et jamais après sa libération (§8.2 : jamais réutilisé).
func TestClaimIgnoreLesPodsNonJoignablesEtLiberes(t *testing.T) {
	dsn := startPostgres(t)
	ctx := context.Background()

	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	uid := seedUser(t, s, "alice")
	pool := core.PodPool{UserID: uid, Repo: "infra", Environment: "staging"}
	runID := seedRunFor(t, s, uid, testULID)

	// Enregistré mais jamais connecté : le contrôleur a créé le pod, le canal
	// n'est pas ouvert.
	jamaisPret, _ := seedPod(t, s, "warden-runner-froid", pool, false)
	if _, err := s.ClaimPod(ctx, pool, runID); !errors.Is(err, store.ErrNoWarmPod) {
		t.Fatalf("un pod non connecté a été réclamé: %v", err)
	}

	// Connecté, puis déconnecté : il redevient non réclamable.
	if err := s.SetPodReady(ctx, jamaisPret, true); err != nil {
		t.Fatalf("SetPodReady(true): %v", err)
	}
	if err := s.SetPodReady(ctx, jamaisPret, false); err != nil {
		t.Fatalf("SetPodReady(false): %v", err)
	}
	if _, err := s.ClaimPod(ctx, pool, runID); !errors.Is(err, store.ErrNoWarmPod) {
		t.Fatalf("un pod déconnecté a été réclamé: %v", err)
	}

	// Libéré : définitif. Ni réclamable, ni re-marquable joignable.
	libere, tok := seedPod(t, s, "warden-runner-mort", pool, true)
	if err := s.ReleasePod(ctx, libere); err != nil {
		t.Fatalf("ReleasePod: %v", err)
	}
	if err := s.SetPodReady(ctx, libere, true); err == nil {
		t.Error("un pod libéré a pu être remarqué joignable")
	}
	if _, err := s.ClaimPod(ctx, pool, runID); !errors.Is(err, store.ErrNoWarmPod) {
		t.Fatalf("un pod libéré a été réclamé: %v", err)
	}
	if _, err := s.AuthenticatePod(ctx, tok); !errors.Is(err, store.ErrPodUnknown) {
		t.Errorf("le jeton d'un pod libéré est encore accepté: %v", err)
	}

	// Libérer deux fois n'est pas une erreur : les appelants sont des chemins
	// de nettoyage.
	if err := s.ReleasePod(ctx, libere); err != nil {
		t.Errorf("seconde libération: %v", err)
	}
}

// Le point central de l'ADR-0023 : le serveur résout `pod -> run` depuis sa
// propre base. Un `run_id` reçu dans un corps de requête n'est jamais la source.
func TestAuthentificationResoutLeRunCoteServeur(t *testing.T) {
	dsn := startPostgres(t)
	ctx := context.Background()

	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	uid := seedUser(t, s, "alice")
	pool := core.PodPool{UserID: uid, Repo: "infra", Environment: "staging"}
	podID, tok := seedPod(t, s, "warden-runner-aaaa", pool, true)
	runID := seedRunFor(t, s, uid, testULID)

	// Avant le claim : authentifié, mais sans run. C'est l'état NORMAL d'un pod
	// chaud, et il ne doit rien pouvoir en tirer.
	id, err := s.AuthenticatePod(ctx, tok)
	if err != nil {
		t.Fatalf("AuthenticatePod: %v", err)
	}
	if id.PodID != podID {
		t.Errorf("PodID = %d, attendu %d", id.PodID, podID)
	}
	if id.Claimed() {
		t.Errorf("un pod non réclamé porte le run %d", id.RunID)
	}

	if _, err := s.ClaimPod(ctx, pool, runID); err != nil {
		t.Fatalf("ClaimPod: %v", err)
	}

	id, err = s.AuthenticatePod(ctx, tok)
	if err != nil {
		t.Fatalf("AuthenticatePod après claim: %v", err)
	}
	if id.RunID != runID {
		t.Errorf("run résolu = %d, attendu %d", id.RunID, runID)
	}

	// Un jeton qui n'a jamais été émis, et le jeton nul.
	autre, err := core.NewPodToken()
	if err != nil {
		t.Fatalf("NewPodToken: %v", err)
	}
	if _, err := s.AuthenticatePod(ctx, autre); !errors.Is(err, store.ErrPodUnknown) {
		t.Errorf("un jeton inconnu a été accepté: %v", err)
	}
	if _, err := s.AuthenticatePod(ctx, core.PodToken{}); !errors.Is(err, store.ErrPodUnknown) {
		t.Errorf("le jeton nul a été accepté: %v", err)
	}
}

// Le routage de l'ADR-0018 amendée : la notification porte un POINTEUR — l'id
// du pod — et jamais l'ordre. Ce test tient cette arête, que le compilateur ne
// tient pas : le nom du canal est dupliqué entre la migration et l'adaptateur.
func TestNotificationDeDispatchPorteUnPointeur(t *testing.T) {
	dsn := startPostgres(t)
	ctx := context.Background()

	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	uid := seedUser(t, s, "alice")
	pool := core.PodPool{UserID: uid, Repo: "infra", Environment: "staging"}
	podID, _ := seedPod(t, s, "warden-runner-aaaa", pool, true)
	runID := seedRunFor(t, s, uid, testULID)

	ecoute := connect(t, dsn)
	defer ecoute.Close(ctx)
	if _, err := ecoute.Exec(ctx, "LISTEN warden_pod_dispatch"); err != nil {
		t.Fatalf("LISTEN: %v", err)
	}

	attendre := func(quoi string) string {
		t.Helper()
		borne, annule := context.WithTimeout(ctx, 5*time.Second)
		defer annule()
		n, err := ecoute.WaitForNotification(borne)
		if err != nil {
			t.Fatalf("aucune notification pour %s: %v", quoi, err)
		}
		return n.Payload
	}

	if _, err := s.ClaimPod(ctx, pool, runID); err != nil {
		t.Fatalf("ClaimPod: %v", err)
	}
	if p := attendre("le claim"); p != strconv.FormatInt(podID, 10) {
		t.Errorf("charge utile du claim = %q, attendu l'id du pod %d", p, podID)
	}

	if err := s.RequestPodCancel(ctx, runID); err != nil {
		t.Fatalf("RequestPodCancel: %v", err)
	}
	if p := attendre("l'annulation"); p != strconv.FormatInt(podID, 10) {
		t.Errorf("charge utile de l'annulation = %q, attendu l'id du pod %d", p, podID)
	}

	// Une seconde demande d'annulation ne renotifie pas : `coalesce` fige la
	// première, et le trigger ne se déclenche qu'à la transition.
	if err := s.RequestPodCancel(ctx, runID); err != nil {
		t.Fatalf("seconde RequestPodCancel: %v", err)
	}
	borne, annule := context.WithTimeout(ctx, 700*time.Millisecond)
	defer annule()
	if n, err := ecoute.WaitForNotification(borne); err == nil {
		t.Errorf("une seconde demande d'annulation a renotifié (%q)", n.Payload)
	}

	// Et l'état lu par le réplica dit bien les deux choses.
	etats, err := s.PodStates(ctx, []int64{podID})
	if err != nil {
		t.Fatalf("PodStates: %v", err)
	}
	if len(etats) != 1 {
		t.Fatalf("PodStates a rendu %d lignes", len(etats))
	}
	if etats[0].RunID != runID || !etats[0].CancelRequested() || etats[0].Released() {
		t.Errorf("état lu = %+v", etats[0])
	}
}

// WatchPodDispatch est ce qui relie la notification au réplica qui tient la
// connexion. Le nom du canal y est DUPLIQUÉ depuis la migration — deux
// adaptateurs sortants n'ayant pas le droit de se connaître (ADR-0019) — et un
// renommage casserait le routage EN SILENCE, puisque le poll du dispatcher
// continuerait de tout servir. C'est ce test qui tient cette arête, pas le
// compilateur.
func TestWatchPodDispatchReveilleSurLeClaim(t *testing.T) {
	dsn := startPostgres(t)
	ctx := context.Background()

	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	uid := seedUser(t, s, "alice")
	pool := core.PodPool{UserID: uid, Repo: "infra", Environment: "staging"}
	podID, _ := seedPod(t, s, "warden-runner-aaaa", pool, true)
	runID := seedRunFor(t, s, uid, testULID)

	reveils := make(chan int64, 4)
	ecoute, arreter := context.WithCancel(ctx)
	defer arreter()
	go func() { _ = s.WatchPodDispatch(ecoute, func(id int64) { reveils <- id }) }()

	// Attendre que le LISTEN soit effectivement posé : la connexion d'écoute
	// est hors du pool, donc invisible dans ses métriques — c'est à cela que
	// sert son `application_name`.
	attendreEcoute(t, dsn)

	if _, err := s.ClaimPod(ctx, pool, runID); err != nil {
		t.Fatalf("ClaimPod: %v", err)
	}
	select {
	case id := <-reveils:
		if id != podID {
			t.Errorf("réveil sur le pod %d, attendu %d", id, podID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("aucun réveil après le claim")
	}

	if err := s.RequestPodCancel(ctx, runID); err != nil {
		t.Fatalf("RequestPodCancel: %v", err)
	}
	select {
	case id := <-reveils:
		if id != podID {
			t.Errorf("réveil d'annulation sur le pod %d, attendu %d", id, podID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("aucun réveil après la demande d'annulation")
	}
}

// attendreEcoute bloque jusqu'à ce que la connexion d'écoute du dispatch soit
// posée et oisive, c'est-à-dire en attente de notifications.
func attendreEcoute(t *testing.T, dsn string) {
	t.Helper()
	ctx := context.Background()
	c := connect(t, dsn)
	defer c.Close(ctx)

	fin := time.Now().Add(10 * time.Second)
	for time.Now().Before(fin) {
		var n int
		if err := c.QueryRow(ctx,
			`SELECT count(*) FROM pg_stat_activity
			  WHERE application_name = 'warden-dispatch-listen' AND state = 'idle'`).Scan(&n); err != nil {
			t.Fatalf("pg_stat_activity: %v", err)
		}
		if n > 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("la connexion d'écoute du dispatch ne s'est pas établie")
}

// Le pendant, pour les pods, du test négatif de l'ADR-0008 : le rôle
// d'ingestion résout `pod -> run` et rien d'autre. En particulier il ne peut pas
// s'attribuer un run — sans quoi le domaine hostile choisirait lui-même le run
// dont il écrit le journal.
func TestRoleIngestionNePeutPasSeReclamerUnPod(t *testing.T) {
	dsn := startPostgres(t)
	ctx := context.Background()

	admin := connect(t, dsn)
	defer admin.Close(ctx)
	if _, err := admin.Exec(ctx, `CREATE ROLE ingest_login LOGIN PASSWORD 'ingest'`); err != nil {
		t.Fatalf("création du rôle de connexion: %v", err)
	}
	if _, err := admin.Exec(ctx, `GRANT warden_ingest TO ingest_login`); err != nil {
		t.Fatalf("GRANT: %v", err)
	}

	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	uid := seedUser(t, s, "alice")
	pool := core.PodPool{UserID: uid, Repo: "infra", Environment: "staging"}
	podID, tok := seedPod(t, s, "warden-runner-aaaa", pool, true)
	runID := seedRunFor(t, s, uid, testULID)
	if _, err := s.ClaimPod(ctx, pool, runID); err != nil {
		t.Fatalf("ClaimPod: %v", err)
	}

	restreint := connect(t, swapUser(dsn, "ingest_login", "ingest"))
	defer restreint.Close(ctx)

	// Ce qu'il DOIT pouvoir faire : résoudre son run depuis son jeton.
	var vu int64
	if err := restreint.QueryRow(ctx,
		`SELECT run_id FROM runner_pod WHERE token_hash = $1 AND released_at IS NULL`,
		tok.Hash()).Scan(&vu); err != nil {
		t.Fatalf("le rôle d'ingestion ne peut pas résoudre son run: %v", err)
	}
	if vu != runID {
		t.Errorf("run résolu = %d, attendu %d", vu, runID)
	}

	interdits := []struct {
		nom  string
		q    string
		args []any
	}{
		{"lire la clé de pool", `SELECT user_id, repo, environment FROM runner_pod`, nil},
		{"lire la ligne entière", `SELECT * FROM runner_pod WHERE id = $1`, []any{podID}},
		{"s'attribuer un run", `UPDATE runner_pod SET run_id = $2 WHERE id = $1`, []any{podID, runID}},
		{"se déclarer joignable", `UPDATE runner_pod SET ready_at = now() WHERE id = $1`, []any{podID}},
		{"s'enregistrer un pod", `INSERT INTO runner_pod (pod_name, user_id, repo, environment, token_hash) VALUES ('x', 1, 'r', 'e', '\x00')`, nil},
		{"effacer un pod", `DELETE FROM runner_pod WHERE id = $1`, []any{podID}},
	}
	for _, c := range interdits {
		if _, err := restreint.Exec(ctx, c.q, c.args...); err == nil {
			t.Errorf("le rôle d'ingestion peut %s — le GRANT est trop large", c.nom)
		}
	}
}

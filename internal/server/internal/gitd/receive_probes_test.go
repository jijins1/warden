// Phase A probes: what git actually does on the write edge.
//
// No production code is exercised here. These tests measure the ENGINE, not
// warden, and they exist because the previous lot proved that reasoning about
// git from its documentation is not reliable — three assumptions were tested,
// two were wrong, and both failed silently rather than loudly.
//
// They are kept in the tree afterwards for the same reason TestProtocolIsPinnedToV0
// is kept: they are the tripwire for a git upgrade. Measured on the version
// reported by TestProbeA0GitVersion, which every other probe's reading is
// relative to.
//
// The repository requires git 2.45 (ADR-0005, internal/git/version.go) while a
// development machine may carry less. These probes deliberately do not call
// CheckVersion — quarantine has existed since 2.11 — but any reading that could
// plausibly differ between versions must be reported as such, never absorbed.

package gitd_test

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// probeConfig is extra git configuration applied to the served repository.
type probeConfig [][2]string

// gitCommand builds a git command with the same neutralised environment runGit
// uses, for the cases that need to write on stdin.
func gitCommand(t *testing.T, dir string, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), gitBinary(t), args...) // #nosec G204 -- literal test arguments
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=/usr/bin:/bin",
		"HOME=" + dir,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=warden", "GIT_AUTHOR_EMAIL=warden@invalid",
		"GIT_COMMITTER_NAME=warden", "GIT_COMMITTER_EMAIL=warden@invalid",
		"LC_ALL=C",
	}
	return cmd
}

// probeServer serves git http-backend over HTTP with write enabled.
//
// Deliberately NOT gitd's own handler: these probes measure git's behaviour, and
// routing them through our transport would confuse "git does X" with "our code
// lets git do X". The hardening here is only what a probe needs to be
// reproducible.
func probeServer(t *testing.T, root, bare string, cfg probeConfig) string {
	t.Helper()

	env := []string{
		"PATH=/usr/bin:/bin",
		"LC_ALL=C",
		"GIT_PROJECT_ROOT=" + root,
		"GIT_HTTP_EXPORT_ALL=1",
		"HOME=" + root,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		// Without this, http-backend refuses receive-pack outright when
		// REMOTE_USER is unset — the push would fail for a reason that has
		// nothing to do with what we are measuring.
		"GIT_COMMITTER_NAME=warden", "GIT_COMMITTER_EMAIL=warden@invalid",

		// Probe A1b: the hook is a separate PROCESS, so the only channel that can
		// carry the authorization scope to it is the environment, across
		// CGI -> http-backend -> receive-pack -> hook. Three exec boundaries, any
		// of which could filter it.
		"WARDEN_PROBE_SCOPE=carried-all-the-way",
	}

	full := append(probeConfig{{"http.receivepack", "true"}}, cfg...)
	env = append(env, fmt.Sprintf("GIT_CONFIG_COUNT=%d", len(full)))
	for i, kv := range full {
		env = append(env,
			fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i, kv[0]),
			fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, kv[1]),
		)
	}

	backend := &cgi.Handler{
		Path: gitBinary(t),
		Args: []string{"http-backend"},
		Dir:  root,
		Env:  env,
	}
	srv := httptest.NewServer(backend)
	t.Cleanup(srv.Close)

	return srv.URL + "/" + filepath.Base(bare)
}

// writeHook installs an executable pre-receive hook and returns its directory.
func writeHook(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	hook := filepath.Join(dir, "pre-receive")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\n"+body), 0o700); err != nil { // #nosec G306 -- a hook must be executable
		t.Fatalf("writing hook: %v", err)
	}
	return dir
}

// objectFiles lists every object file in the repository, quarantine included.
//
// It returns two sets on purpose: what is durably in the store, and what is
// sitting in an incoming-* directory. Probe A2 turns on the difference.
// apparus rend les entrées présentes dans `apres` et absentes d'`avant`.
//
// Les disparitions sont IGNORÉES délibérément : elles ne peuvent pas témoigner
// qu'un objet a survécu, et git en produit de lui-même — un `gc --auto` se
// détache en arrière-plan par défaut et peut empaqueter des objets épars entre
// deux relevés.
func apparus(avant, apres []string) []string {
	connus := make(map[string]struct{}, len(avant))
	for _, f := range avant {
		connus[f] = struct{}{}
	}
	var neufs []string
	for _, f := range apres {
		if _, deja := connus[f]; !deja {
			neufs = append(neufs, f)
		}
	}
	return neufs
}

func objectFiles(t *testing.T, bare string) (store, incoming []string) {
	t.Helper()
	objects := filepath.Join(bare, "objects")

	err := filepath.Walk(objects, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil //nolint:nilerr // a vanished temporary directory is a reading, not a failure
		}
		rel, _ := filepath.Rel(objects, path)
		if strings.HasPrefix(rel, "info/") {
			return nil
		}
		if strings.HasPrefix(rel, "incoming-") {
			incoming = append(incoming, rel)
			return nil
		}
		store = append(store, rel)
		return nil
	})
	if err != nil {
		t.Fatalf("walking objects: %v", err)
	}
	sort.Strings(store)
	sort.Strings(incoming)
	return store, incoming
}

// oneCommitRepo builds a bare repository holding a single commit, and a working
// clone alongside it.
func oneCommitRepo(t *testing.T) (root, bare, src, sha string) {
	t.Helper()
	root = t.TempDir()
	bare = filepath.Join(root, "depot.git")
	mustGit(t, root, "init", "--bare", "--initial-branch=main", bare)
	// `gc.auto=0` : par défaut `gc.autoDetach` vaut true, donc un `gc --auto`
	// déclenché par le premier push continue EN ARRIÈRE-PLAN et peut modifier
	// `objects/` entre deux relevés d'une sonde. On mesure l'effet du push, pas
	// une course contre la maintenance.
	mustGit(t, bare, "config", "gc.auto", "0")

	src = t.TempDir()
	mustGit(t, src, "init", "--initial-branch=main", ".")
	if err := os.WriteFile(filepath.Join(src, "file.txt"), []byte("mainline"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	mustGit(t, src, "add", "file.txt")
	mustGit(t, src, "commit", "-m", "mainline")
	sha = strings.TrimSpace(mustGit(t, src, "rev-parse", "HEAD"))
	mustGit(t, src, "push", bare, "HEAD:refs/heads/main")

	return root, bare, src, sha
}

// --- A0 -------------------------------------------------------------------

// TestProbeA0GitVersion records the version every other reading is relative to.
func TestProbeA0GitVersion(t *testing.T) {
	out := mustGit(t, t.TempDir(), "version")
	t.Logf("READING A0: %s", strings.TrimSpace(out))
}

// --- A1 -------------------------------------------------------------------

// TestProbeA1QuarantineEnvironment answers what pre-receive is actually handed.
//
// The freshness control of the next lot is written as "list the new objects,
// then check each one is absent from the real store". That phrasing only works
// if the hook can tell the two apart from its environment. If it cannot, the
// control has to enumerate the quarantine directory instead, which would bind us
// to an undocumented layout.
func TestProbeA1QuarantineEnvironment(t *testing.T) {
	root, bare, src, _ := oneCommitRepo(t)

	dump := filepath.Join(t.TempDir(), "env.txt")
	hooks := writeHook(t, "env > "+dump+"\nexit 0\n")
	url := probeServer(t, root, bare, probeConfig{{"core.hooksPath", hooks}})

	mustGit(t, src, "commit", "--allow-empty", "-m", "second")
	if out, err := runGit(t, src, "push", url, "HEAD:refs/heads/probe"); err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}

	raw, err := os.ReadFile(dump) // #nosec G304 -- path built by the test
	if err != nil {
		t.Fatalf("reading the hook's environment: %v", err)
	}

	interesting := []string{
		"GIT_QUARANTINE_PATH",
		"GIT_OBJECT_DIRECTORY",
		"GIT_ALTERNATE_OBJECT_DIRECTORIES",
		"GIT_DIR",
		"GIT_PUSH_CERT",
		"WARDEN_PROBE_SCOPE",
	}
	seen := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		for _, key := range interesting {
			if strings.HasPrefix(line, key+"=") {
				seen[key] = strings.TrimPrefix(line, key+"=")
			}
		}
	}

	for _, key := range interesting {
		if v, ok := seen[key]; ok {
			t.Logf("READING A1: %s=%s", key, v)
		} else {
			t.Logf("READING A1: %s ABSENT", key)
		}
	}

	// A1b — without this, the scope has to travel some other way, and every other
	// way is worse: a file the hook reads (racy between concurrent pushes), or a
	// call back into gitd (a socket, and a second authorization surface).
	if seen["WARDEN_PROBE_SCOPE"] != "carried-all-the-way" {
		t.Error("FINDING A1b: a custom environment variable does NOT survive to the hook. " +
			"The scope cannot be carried by the environment; redesign required.")
	} else {
		t.Logf("READING A1b: custom environment survives CGI -> http-backend -> receive-pack -> hook")
	}

	// Does the quarantine carry its own alternates pointing back at the store?
	// This decides whether a plain rev-list inside the hook sees both.
	if q, ok := seen["GIT_QUARANTINE_PATH"]; ok && q != "" {
		alt := filepath.Join(q, "info", "alternates")
		if content, err := os.ReadFile(alt); err == nil { // #nosec G304 -- path from git
			t.Logf("READING A1: quarantine info/alternates = %q", strings.TrimSpace(string(content)))
		} else {
			t.Logf("READING A1: quarantine has NO info/alternates (%v)", err)
		}
	} else {
		t.Error("FINDING A1: no GIT_QUARANTINE_PATH — the freshness control cannot " +
			"distinguish quarantine from store by environment alone")
	}
}

// --- A2 -------------------------------------------------------------------

// TestProbeA2RefusalLeavesNothing checks the guarantee the whole design rests on.
//
// §5.3 requires the secret scan to happen BEFORE persistence. That is only true
// if a refusing pre-receive leaves the object store untouched. The property is
// asserted everywhere and verified nowhere.
func TestProbeA2RefusalLeavesNothing(t *testing.T) {
	root, bare, src, _ := oneCommitRepo(t)

	hooks := writeHook(t, "echo 'refused by probe' >&2\nexit 1\n")
	url := probeServer(t, root, bare, probeConfig{{"core.hooksPath", hooks}})

	before, _ := objectFiles(t, bare)

	if err := os.WriteFile(filepath.Join(src, "secret.txt"), []byte("a blob that must not survive"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	mustGit(t, src, "add", "secret.txt")
	mustGit(t, src, "commit", "-m", "carries a blob")

	out, err := runGit(t, src, "push", url, "HEAD:refs/heads/probe")
	if err == nil {
		t.Fatalf("the push was accepted although pre-receive refused:\n%s", out)
	}
	t.Logf("READING A2: client saw: %s", strings.TrimSpace(out))

	after, incoming := objectFiles(t, bare)

	// La propriété du §5.3 porte sur ce qui APPARAÎT, pas sur la taille du
	// magasin. Comparer deux nombres confondait « un objet du push refusé a
	// survécu » avec « le compte a changé » — et rendait donc un échec sur une
	// DIMINUTION, que la maintenance de git peut produire à tout moment. Le
	// message allait jusqu'à annoncer « -1 object file(s) SURVIVED », ce qui ne
	// veut rien dire.
	//
	// La bonne lecture est la différence ensembliste : seuls les fichiers
	// présents après et absents avant témoignent d'une persistance.
	survivants := apparus(before, after)
	if len(survivants) != 0 {
		t.Errorf("FINDING A2: %d object file(s) SURVIVED a refused push: %v",
			len(survivants), survivants)
	} else {
		t.Logf("READING A2: no object from the refused push survived (%d before, %d after)",
			len(before), len(after))
	}
	if len(incoming) != 0 {
		t.Logf("READING A2: %d file(s) left behind in incoming-*: %v", len(incoming), incoming)
	} else {
		t.Logf("READING A2: no incoming-* residue")
	}

	// The ref must not exist either.
	if out, err := runGit(t, bare, "rev-parse", "--verify", "refs/heads/probe"); err == nil {
		t.Errorf("FINDING A2: the ref was created despite the refusal: %s", out)
	}
}

// --- A3 -------------------------------------------------------------------

// TestProbeA3ClaimAnObjectWithoutSendingIt is the probe that justifies the lot.
//
// It is the first attack vector of gitnamespaces(7), performed for real: "The
// attacker chooses an object ID X to steal and sends a ref to X, but isn't
// required to send the content of X because the victim already has it."
//
// A stock `git push` cannot express this — it refuses to name an object it does
// not hold — so the request is built by hand. That is not a reason to dismiss it:
// the threat model of §2 is arbitrary code, and an attacker who can run a
// playbook can certainly craft forty bytes of pkt-line.
//
// EXPECTED TO FAIL until the freshness control of the next lot exists. A red
// result here is the measurement, not a defect.
func TestProbeA3ClaimAnObjectWithoutSendingIt(t *testing.T) {
	root, bare, _, victimSHA := oneCommitRepo(t)
	url := probeServer(t, root, bare, nil)

	// receiveCommand and emptyPack live in push_test.go: the probe and the
	// control it justifies must speak byte-for-byte the same wire format, or the
	// pair proves nothing.
	payload := append([]byte(receiveCommand(zeroOID, victimSHA, "refs/heads/stolen")), emptyPack()...)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		url+"/git-receive-pack", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-git-receive-pack-request")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	answer, _ := io.ReadAll(resp.Body)

	t.Logf("READING A3: status %d, server said: %q", resp.StatusCode, string(answer))

	created, err := runGit(t, bare, "rev-parse", "--verify", "refs/heads/stolen")
	confirmed := err == nil && strings.TrimSpace(created) == victimSHA

	if confirmed {
		t.Logf("FINDING A3 — VECTOR CONFIRMED: a ref was created pointing at %s, "+
			"an object the client never sent. In default configuration git accepts this.", victimSHA)
	}

	// The assertion is on the MEASUREMENT, not on the desired behaviour, and the
	// direction is deliberate.
	//
	// A probe left permanently red would be a broken build, and a broken build is
	// how a real signal gets ignored. So this pins what git does TODAY: if it ever
	// starts refusing on its own, this goes red and tells us the ground moved —
	// which is exactly what a tripwire is for.
	//
	// Phase B does NOT invert this test. It adds the mirror-image assertion in
	// push_test.go: the same crafted request, refused by the freshness control.
	// The two then coexist, one saying "git alone permits it", the other "warden
	// does not" — and that pair is the entire justification of the control.
	if !confirmed {
		t.Errorf("this git refused the claim unaided, unlike 2.43. " +
			"The freshness control may be redundant — revisit ADR-0027 before relying on this.")
	}
}

// --- A4 -------------------------------------------------------------------

// TestProbeA4HideRefs asks whether hideRefs is a curtain or a lock.
//
// The design must not lean on it for anything but anti-enumeration. If it also
// refused writes it would be a second line of defence; if it only hides, then a
// client naming a hidden ref outright can still update it, and the hook is the
// only control. Measured under both protocol versions because the previous lot
// found the two diverging on exactly this kind of setting.
func TestProbeA4HideRefs(t *testing.T) {
	for _, version := range []string{"0", "2"} {
		t.Run("protocol v"+version, func(t *testing.T) {
			root, bare, src, _ := oneCommitRepo(t)
			mustGit(t, src, "push", bare, "HEAD:refs/audit/someone-else/run")

			url := probeServer(t, root, bare, probeConfig{{"receive.hideRefs", "refs/audit"}})

			// The receive-pack advertisement has no porcelain: `ls-remote` only
			// speaks upload-pack. Reading it over HTTP is both simpler and closer
			// to what a hostile client would actually do.
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
				url+"/info/refs?service=git-receive-pack", nil)
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			req.Header.Set("Git-Protocol", "version="+version)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("advertisement: %v", err)
			}
			raw, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()

			advertised := string(raw)
			hidden := !strings.Contains(advertised, "refs/audit/")
			t.Logf("READING A4/v%s: hidden from the advertisement: %v", version, hidden)

			mustGit(t, src, "commit", "--allow-empty", "-m", "overwrite attempt")
			out, err := gitWithToken(t, src, "",
				"-c", "protocol.version="+version,
				"push", "--force", url, "HEAD:refs/audit/someone-else/run")
			if err == nil {
				t.Logf("READING A4/v%s: WRITE ACCEPTED on a hidden ref — hideRefs is a curtain, not a lock", version)
			} else {
				t.Logf("READING A4/v%s: write REFUSED — %s", version,
					strings.ReplaceAll(strings.TrimSpace(out), "\n", " | "))
			}
		})
	}
}

// --- A5 -------------------------------------------------------------------

// TestProbeA5FsckObjects measures the cost of receive.fsckObjects on history the
// product will genuinely receive.
//
// ADR-0003 makes the audit commit carry whatever local history the server lacks.
// Real repositories hold old commits from assorted tools, and fsck is strict
// about things that are cosmetic. The risk is a product that refuses to audit
// somebody's repository. This probe crafts one malformed-but-harmless commit and
// reports exactly which message id fires, so a severity table can be written from
// evidence rather than from a list found on the internet.
func TestProbeA5FsckObjects(t *testing.T) {
	_, _, src, parent := oneCommitRepo(t)

	tree := strings.TrimSpace(mustGit(t, src, "rev-parse", "HEAD^{tree}"))

	// `--literally` is what lets a malformed object be written at all: no space
	// before the angle bracket, which is `missingSpaceBeforeEmail`.
	raw := fmt.Sprintf("tree %s\nparent %s\nauthor Old Tool<tool@invalid> 1000000000 +0000\n"+
		"committer Old Tool<tool@invalid> 1000000000 +0000\n\nlegacy commit\n", tree, parent)

	cmd := gitCommand(t, src, "hash-object", "-t", "commit", "-w", "--stdin", "--literally")
	cmd.Stdin = strings.NewReader(raw)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Skipf("this git will not write the malformed object, probe not applicable: %v\n%s", err, out)
	}
	bad := strings.TrimSpace(string(out))
	t.Logf("READING A5: malformed commit written locally as %s", bad)

	for _, cfg := range []struct {
		name string
		cfg  probeConfig
	}{
		{"fsckObjects off", nil},
		{"fsckObjects on", probeConfig{{"receive.fsckObjects", "true"}}},
	} {
		t.Run(cfg.name, func(t *testing.T) {
			freshRoot, freshBare, freshSrc, _ := oneCommitRepo(t)
			// Re-create the malformed object in a repository that can reach the
			// fresh server.
			freshTree := strings.TrimSpace(mustGit(t, freshSrc, "rev-parse", "HEAD^{tree}"))
			freshParent := strings.TrimSpace(mustGit(t, freshSrc, "rev-parse", "HEAD"))
			freshRaw := fmt.Sprintf("tree %s\nparent %s\nauthor Old Tool<tool@invalid> 1000000000 +0000\n"+
				"committer Old Tool<tool@invalid> 1000000000 +0000\n\nlegacy commit\n", freshTree, freshParent)
			c := gitCommand(t, freshSrc, "hash-object", "-t", "commit", "-w", "--stdin", "--literally")
			c.Stdin = strings.NewReader(freshRaw)
			o, err := c.CombinedOutput()
			if err != nil {
				t.Skipf("cannot write malformed object: %v\n%s", err, o)
			}
			freshBad := strings.TrimSpace(string(o))

			url := probeServer(t, freshRoot, freshBare, cfg.cfg)
			pushed, err := runGit(t, freshSrc, "push", url, freshBad+":refs/heads/legacy")
			if err != nil {
				t.Logf("READING A5 (%s): REFUSED — %s", cfg.name, strings.TrimSpace(pushed))
			} else {
				t.Logf("READING A5 (%s): accepted", cfg.name)
			}
		})
	}
}

// Stage 1 of ADR-0010, write edge. Real bare repositories, a real git client,
// and — where a stock client refuses to express the attack — hand-built
// receive-pack requests.
//
// The forged requests are not a contrivance. §2 states the threat model as
// arbitrary code, and ADR-0027 measured that git in default configuration
// accepts a ref pointing at an object the client never sent. A test suite that
// only drove `git push` would conclude the product is safe because the honest
// client is honest.

package gitd_test

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha1" // #nosec G505 -- git's pack format is defined in terms of SHA-1
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jijins1/warden/internal/git"
	"github.com/jijins1/warden/internal/server/internal/gitd"
	"github.com/jijins1/warden/internal/server/internal/gitwire"
)

// TestMain lets this binary act as the pre-receive hook.
//
// The hook is a separate process by nature, so testing its logic through
// anything other than a real process would test something else. git invokes it
// through a symlink named pre-receive; dispatching on argv[0] here is the same
// mechanism wardend uses in production, exercised by the same code path.
func TestMain(m *testing.M) {
	if filepath.Base(os.Args[0]) == "pre-receive" {
		os.Exit(gitd.PreReceive(context.Background(), os.Stdin, os.Stderr))
	}
	os.Exit(m.Run())
}

const (
	mainlineRef = "refs/heads/main"
	alicePrefix = "refs/audit/alice/"
	aliceRef    = "refs/audit/alice/01JZQ0000000000000000ALIC"
	bobRef      = "refs/audit/bob/01JZQ00000000000000000BOB"
	pushToken   = "alice-push-token"
)

// fixedPushAuthorizer hands out one scope, for one token.
type fixedPushAuthorizer struct {
	token string
	scope gitd.PushScope
}

func (a fixedPushAuthorizer) AuthorizePush(_ context.Context, token string) (gitd.PushScope, error) {
	if token != a.token {
		return gitd.PushScope{}, errors.New("unknown token")
	}
	return a.scope, nil
}

// writeWorld builds the situation every write test starts from.
//
// One repository holding the mainline and BOB's audit ref, and a client that has
// the mainline only. Bob's tree is the thing Alice must never obtain, and she
// legitimately knows its SHA — ADR-0026 established that a SHA is not a secret.
func writeWorld(t *testing.T) (root, bare, alice, mainSHA, bobTree string) {
	t.Helper()

	root = t.TempDir()
	bare = filepath.Join(root, "depot.git")
	mustGit(t, root, "init", "--bare", "--initial-branch=main", bare)

	seed := t.TempDir()
	mustGit(t, seed, "init", "--initial-branch=main", ".")
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(seed, name), []byte(content), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	write("playbook.yml", "- hosts: all")
	mustGit(t, seed, "add", ".")
	mustGit(t, seed, "commit", "-m", "mainline")
	mainSHA = strings.TrimSpace(mustGit(t, seed, "rev-parse", "HEAD"))
	mustGit(t, seed, "push", bare, mainSHA+":"+mainlineRef)

	// Bob's audit commit: a tree Alice has never seen and must not obtain.
	write("bob-secret-inventory.ini", "[prod]\ndb-01 ansible_host=10.0.0.9")
	mustGit(t, seed, "add", ".")
	mustGit(t, seed, "commit", "-m", "bob's run")
	bobSHA := strings.TrimSpace(mustGit(t, seed, "rev-parse", "HEAD"))
	bobTree = strings.TrimSpace(mustGit(t, seed, "rev-parse", "HEAD^{tree}"))
	mustGit(t, seed, "push", bare, bobSHA+":"+bobRef)

	// Alice clones the mainline only, which is all the server would ever serve
	// her.
	alice = t.TempDir()
	mustGit(t, alice, "init", "--initial-branch=main", ".")
	mustGit(t, alice, "fetch", bare, mainlineRef)
	mustGit(t, alice, "reset", "--hard", "FETCH_HEAD")

	return root, bare, alice, mainSHA, bobTree
}

// servePush mounts the write transport with a scope granting exactly one ref.
func servePush(t *testing.T, root string, scope gitd.PushScope) string {
	t.Helper()
	gitBinary(t)

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("locating the test binary: %v", err)
	}

	s, err := gitd.New(gitd.Config{
		Racine:         root,
		Jeton:          jetonDeTest,
		PushAuthorizer: fixedPushAuthorizer{token: pushToken, scope: scope},
		HooksDir:       filepath.Join(t.TempDir(), "hooks"),
		HookExecutable: exe,
	})
	if err != nil {
		t.Fatalf("gitd.New: %v", err)
	}

	srv := httptest.NewServer(s.PushHandler())
	t.Cleanup(srv.Close)
	return srv.URL
}

func aliceScope() gitd.PushScope {
	return gitd.PushScope{Repo: "depot", RefPrefix: alicePrefix}
}

// --- the honest path -------------------------------------------------------

// TestPushNominal: a real client, a real audit commit, accepted.
//
// It matters as much as the refusals. A control that refuses everything is not a
// control, and the freshness check runs on this path too — every object Alice
// sends is genuinely new to the server.
func TestPushNominal(t *testing.T) {
	root, bare, alice, _, _ := writeWorld(t)
	url := servePush(t, root, aliceScope())

	if err := os.WriteFile(filepath.Join(alice, "inventory.ini"), []byte("[dev]\nlocalhost"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	mustGit(t, alice, "add", ".")
	mustGit(t, alice, "commit", "-m", "alice's run 01JZQ0000000000000000ALIC")
	auditSHA := strings.TrimSpace(mustGit(t, alice, "rev-parse", "HEAD"))

	out, err := gitWithToken(t, alice, pushToken, "push", url+"/depot.git", auditSHA+":"+aliceRef)
	if err != nil {
		t.Fatalf("the honest push was refused: %v\n%s", err, out)
	}

	got := strings.TrimSpace(mustGit(t, bare, "rev-parse", "--verify", aliceRef))
	if got != auditSHA {
		t.Errorf("ref points at %s, want %s", got, auditSHA)
	}
}

// TestASecondRunSharingATreeIsAccepted is the case the first two runs of a real
// deployment could not reach, because the repository was still empty.
//
// The third run submitted UNCHANGED sources. Its audit commit is new — it names
// its own run — but its tree and its blobs already sit in the store, reachable
// from the previous run's audit ref, which this credential's prefix un-hides.
// Git therefore advertises them, the client rightly omits them, and the
// freshness check must recognise that omission as legitimate.
//
// Refusing here refuses the ordinary case: submitting twice without editing a
// file. Which is what happened — "this push claims 2 object(s) it did not send",
// the tree and the one blob.
func TestASecondRunSharingATreeIsAccepted(t *testing.T) {
	root, bare, alice, mainSHA, _ := writeWorld(t)
	url := servePush(t, root, aliceScope())

	if err := os.WriteFile(filepath.Join(alice, "inventory.ini"), []byte("[dev]\nlocalhost"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	mustGit(t, alice, "add", ".")
	mustGit(t, alice, "commit", "-m", "alice's run 01JZQ0000000000000000ALIC")
	first := strings.TrimSpace(mustGit(t, alice, "rev-parse", "HEAD"))

	if out, err := gitWithToken(t, alice, pushToken, "push", url+"/depot.git",
		first+":"+aliceRef); err != nil {
		t.Fatalf("the first push was refused: %v\n%s", err, out)
	}

	// The second capture, built exactly as the CLI builds it: the SAME tree,
	// parented on the mainline rather than on the previous run. commit-tree is
	// what makes the two commits differ by nothing but their message.
	tree := strings.TrimSpace(mustGit(t, alice, "rev-parse", "HEAD^{tree}"))
	second := strings.TrimSpace(mustGit(t, alice, "commit-tree", tree, "-p", mainSHA,
		"-m", "alice's run 01JZQ0000000000000000ALI2"))
	secondRef := alicePrefix + "01JZQ0000000000000000ALI2"

	out, err := gitWithToken(t, alice, pushToken, "push", url+"/depot.git", second+":"+secondRef)
	if err != nil {
		t.Fatalf("a second run over unchanged sources was refused: %v\n%s", err, out)
	}

	got := strings.TrimSpace(mustGit(t, bare, "rev-parse", "--verify", secondRef))
	if got != second {
		t.Errorf("ref points at %s, want %s", got, second)
	}
}

// --- the first form: claim an object without sending it --------------------

// TestCraftedClaimIsRefused is the mirror of probe A3.
//
// The probe measures that git alone accepts this. This asserts that warden does
// not. The pair is the entire justification of the freshness control: neither
// half means anything without the other.
func TestCraftedClaimIsRefused(t *testing.T) {
	root, bare, _, _, _ := writeWorld(t)
	url := servePush(t, root, aliceScope())

	bobSHA := strings.TrimSpace(mustGit(t, bare, "rev-parse", "--verify", bobRef))

	status, answer := rawReceivePack(t, url+"/depot.git", pushToken,
		receiveCommand(zeroOID, bobSHA, aliceRef), emptyPack())
	t.Logf("server answered %d: %q", status, answer)

	if refExists(t, bare, aliceRef) {
		t.Fatal("LEAK: the audit ref was created pointing at an object the client never sent")
	}
	if !strings.Contains(answer, "did not send") && !strings.Contains(answer, "ng ") {
		t.Errorf("refused, but not by the freshness control — the message was %q", answer)
	}
}

// --- the second form: a fresh commit carrying someone else's tree ----------

// TestStealingAnotherTreeIsRefused is the attack ADR-0027 discovered, and the
// reason the control speaks of EVERY reachable object rather than of the tip.
//
// Alice builds a genuinely new commit — new SHA, so it travels — whose tree is
// Bob's. The tree does not travel, because she does not have it. A check on the
// tip alone sees a brand new object and waves it through; the audit ref then
// points at Bob's code, which Alice reads back through the diff of her own run.
func TestStealingAnotherTreeIsRefused(t *testing.T) {
	root, bare, _, mainSHA, bobTree := writeWorld(t)
	url := servePush(t, root, aliceScope())

	// A commit object naming a tree the sender does not hold. Stock git will not
	// build this, which is exactly why it is built here.
	commit := fmt.Sprintf("tree %s\nparent %s\n"+
		"author Alice <alice@invalid> 1700000000 +0000\n"+
		"committer Alice <alice@invalid> 1700000000 +0000\n\nalice's run\n", bobTree, mainSHA)
	oid := objectID("commit", commit)

	status, answer := rawReceivePack(t, url+"/depot.git", pushToken,
		receiveCommand(zeroOID, oid, aliceRef), packWith(packEntry{kind: objCommit, body: commit}))
	t.Logf("server answered %d: %q", status, answer)

	if refExists(t, bare, aliceRef) {
		t.Fatalf("LEAK: alice's audit ref was created carrying bob's tree %s", bobTree)
	}
}

// --- write-once and the ref allowlist --------------------------------------

// TestAuditRefIsWrittenOnce: the property §6.1 asks of an audit log.
func TestAuditRefIsWrittenOnce(t *testing.T) {
	root, bare, alice, _, _ := writeWorld(t)
	url := servePush(t, root, aliceScope())

	commitAndPush := func(content string) (string, error) {
		if err := os.WriteFile(filepath.Join(alice, "inventory.ini"), []byte(content), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		mustGit(t, alice, "add", ".")
		mustGit(t, alice, "commit", "-m", content)
		sha := strings.TrimSpace(mustGit(t, alice, "rev-parse", "HEAD"))
		out, err := gitWithToken(t, alice, pushToken, "push", url+"/depot.git", sha+":"+aliceRef)
		return out, err
	}

	if out, err := commitAndPush("first"); err != nil {
		t.Fatalf("the first push must succeed: %v\n%s", err, out)
	}
	first := strings.TrimSpace(mustGit(t, bare, "rev-parse", "--verify", aliceRef))

	out, err := commitAndPush("second, overwriting the record of the first")
	if err == nil {
		t.Fatalf("a second push overwrote an audit ref:\n%s", out)
	}

	if now := strings.TrimSpace(mustGit(t, bare, "rev-parse", "--verify", aliceRef)); now != first {
		t.Errorf("the audit ref moved from %s to %s", first, now)
	}
}

// TestRefOutsideScopeIsRefused: the credential names one namespace, and stops
// there.
//
// Note what is NOT in this list: another ref under refs/audit/alice/. ADR-0028
// deliberately traded the exact-ref scope for a prefix, which is what lets the
// CLI mint its own run identifier and removes the reservation round trip. The
// case below named "alicia" is what the trailing slash buys — without it,
// refs/audit/alice would also cover refs/audit/alicia/.
func TestRefOutsideScopeIsRefused(t *testing.T) {
	root, bare, alice, _, _ := writeWorld(t)
	url := servePush(t, root, aliceScope())

	mustGit(t, alice, "commit", "--allow-empty", "-m", "aiming elsewhere")
	sha := strings.TrimSpace(mustGit(t, alice, "rev-parse", "HEAD"))

	for _, target := range []string{
		bobRef,                                  // someone else's audit ref
		mainlineRef,                             // the mainline itself
		"refs/warden/mainline",                  // the freshness anchor
		"refs/namespaces/runs/x/refs/heads/run", // a run's namespace
		"refs/audit/alicia/01JZQ0000000000000000X", // a namespace whose name merely starts like hers
	} {
		t.Run(target, func(t *testing.T) {
			out, err := gitWithToken(t, alice, pushToken, "push", url+"/depot.git", sha+":"+target)
			if err == nil {
				t.Fatalf("push to %s was accepted:\n%s", target, out)
			}
		})
	}

	if refExists(t, bare, "refs/namespaces/runs/x/refs/heads/run") {
		t.Error("a namespace ref was created by a client")
	}
}

// TestAnyRefInOwnNamespaceIsAllowed states the relaxation ADR-0028 accepted,
// rather than leaving it to be discovered.
//
// A prefix scope means the holder may create arbitrary refs under their OWN
// audit namespace. They still cannot read or overwrite anyone else's: the
// freshness control, write-once and hidden refs are untouched. What it buys is
// the removal of an entire round trip and of two-phase run creation.
func TestAnyRefInOwnNamespaceIsAllowed(t *testing.T) {
	root, bare, alice, _, _ := writeWorld(t)
	url := servePush(t, root, aliceScope())

	mustGit(t, alice, "commit", "--allow-empty", "-m", "a run alice named herself")
	sha := strings.TrimSpace(mustGit(t, alice, "rev-parse", "HEAD"))

	const own = "refs/audit/alice/01JZQ000000000000000OTHR"
	if out, err := gitWithToken(t, alice, pushToken, "push", url+"/depot.git", sha+":"+own); err != nil {
		t.Fatalf("alice was refused a ref in her own namespace: %v\n%s", err, out)
	}
	if !refExists(t, bare, own) {
		t.Error("the ref was not created")
	}
}

// TestDeletionIsRefused: an audit log does not lose entries on request.
func TestDeletionIsRefused(t *testing.T) {
	root, bare, alice, _, _ := writeWorld(t)
	url := servePush(t, root, aliceScope())

	mustGit(t, alice, "commit", "--allow-empty", "-m", "to be deleted")
	sha := strings.TrimSpace(mustGit(t, alice, "rev-parse", "HEAD"))
	if out, err := gitWithToken(t, alice, pushToken, "push", url+"/depot.git", sha+":"+aliceRef); err != nil {
		t.Fatalf("setup push: %v\n%s", err, out)
	}

	out, err := gitWithToken(t, alice, pushToken, "push", url+"/depot.git", ":"+aliceRef)
	if err == nil {
		t.Fatalf("the ref was deleted:\n%s", out)
	}
	if !refExists(t, bare, aliceRef) {
		t.Error("the audit ref is gone")
	}
}

// --- what the pusher may see ------------------------------------------------

// TestPusherCannotSeeOthersAuditRefs closes enumeration on the write edge.
//
// The read transport hides other runs behind GIT_NAMESPACE. receive-pack has no
// namespace, so the advertisement would otherwise hand Alice every audit ref in
// the organisation — and ADR-0026 established that knowing a SHA is the whole
// prerequisite of the attacks above.
func TestPusherCannotSeeOthersAuditRefs(t *testing.T) {
	root, _, _, _, _ := writeWorld(t)
	url := servePush(t, root, aliceScope())

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
		url+"/depot.git/info/refs?service=git-receive-pack", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+pushToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("advertisement: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	if strings.Contains(string(body), "refs/audit/bob") {
		t.Errorf("LEAK: bob's audit ref is advertised to alice:\n%s", body)
	}
	if !strings.Contains(string(body), mainlineRef) {
		t.Errorf("the mainline is not advertised; the pusher cannot build on it:\n%s", body)
	}
}

// --- the hooks exception stays bounded --------------------------------------

// TestRepositoryHooksAreNotExecuted guards the one place the product stops
// neutralising hooks.
//
// core.hooksPath is set through the environment, so a hooks/ directory inside a
// bare repository is never consulted. If that ever stops being true, anything
// written into a repository becomes code the role executes.
func TestRepositoryHooksAreNotExecuted(t *testing.T) {
	root, bare, alice, _, _ := writeWorld(t)

	witness := filepath.Join(t.TempDir(), "executed")
	repoHooks := filepath.Join(bare, "hooks")
	if err := os.MkdirAll(repoHooks, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// #nosec G306 -- a hook must be executable to prove it is not executed
	if err := os.WriteFile(filepath.Join(repoHooks, "pre-receive"),
		[]byte("#!/bin/sh\ntouch "+witness+"\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("writing the repository hook: %v", err)
	}

	url := servePush(t, root, aliceScope())
	mustGit(t, alice, "commit", "--allow-empty", "-m", "run")
	sha := strings.TrimSpace(mustGit(t, alice, "rev-parse", "HEAD"))
	_, _ = gitWithToken(t, alice, pushToken, "push", url+"/depot.git", sha+":"+aliceRef)

	if _, err := os.Stat(witness); err == nil {
		t.Fatal("the repository's own pre-receive hook was executed")
	}
}

// --- startup ----------------------------------------------------------------

// TestPushAddrWithoutAuthorizerRefusesToStart: an open write surface is not a
// degradation.
func TestPushAddrWithoutAuthorizerRefusesToStart(t *testing.T) {
	_, err := gitd.New(gitd.Config{
		Racine:   t.TempDir(),
		Jeton:    jetonDeTest,
		PushAddr: ":0",
	})
	if err == nil {
		t.Fatal("a write address without an authorizer started")
	}
}

// TestPushScopeWithoutRefIsRefused: the zero value fails closed, as Scope does.
func TestPushScopeWithoutRefIsRefused(t *testing.T) {
	root, _, _, _, _ := writeWorld(t)
	url := servePush(t, root, gitd.PushScope{Repo: "depot"})

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
		url+"/depot.git/info/refs?service=git-receive-pack", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+pushToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status %d, want 403", resp.StatusCode)
	}
}

// --- wire helpers ------------------------------------------------------------

const zeroOID = "0000000000000000000000000000000000000000"

func refExists(t *testing.T, bare, ref string) bool {
	t.Helper()
	_, err := runGit(t, bare, "rev-parse", "--verify", "--quiet", ref)
	return err == nil
}

// receiveCommand builds the single pkt-line command of a receive-pack request.
func receiveCommand(old, new, ref string) string {
	line := fmt.Sprintf("%s %s %s\x00report-status\n", old, new, ref)
	return fmt.Sprintf("%04x%s", len(line)+4, line) + "0000"
}

// rawReceivePack posts a hand-built receive-pack request.
func rawReceivePack(t *testing.T, url, token, command string, pack []byte) (int, string) {
	t.Helper()
	payload := append([]byte(command), pack...)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		url+"/git-receive-pack", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-git-receive-pack-request")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// git object types as the pack format numbers them.
const (
	objCommit = 1
	objTree   = 2
	objBlob   = 3
)

type packEntry struct {
	kind int
	body string
}

// objectID computes the id git would give an object, without writing it.
func objectID(kind, body string) string {
	h := sha1.New() // #nosec G401 -- git object ids are SHA-1 by definition
	_, _ = fmt.Fprintf(h, "%s %d\x00", kind, len(body))
	_, _ = io.WriteString(h, body)
	return fmt.Sprintf("%x", h.Sum(nil))
}

// emptyPack is a valid packfile carrying no objects at all.
func emptyPack() []byte { return packWith() }

// packWith builds a packfile from raw object bodies.
//
// Written out rather than driven through git because the whole point is to send
// something git would refuse to construct: a commit whose tree is absent.
func packWith(entries ...packEntry) []byte {
	var b bytes.Buffer
	b.WriteString("PACK")
	_ = binary.Write(&b, binary.BigEndian, uint32(2))
	_ = binary.Write(&b, binary.BigEndian, uint32(len(entries)))

	for _, e := range entries {
		size := len(e.body)
		// Type and size share the first byte: 3 bits of type, 4 low bits of size,
		// then 7 bits per continuation byte, MSB marking "more".
		first := byte(e.kind<<4) | byte(size&0x0f)
		size >>= 4
		for size > 0 {
			b.WriteByte(first | 0x80)
			first = byte(size & 0x7f)
			size >>= 7
		}
		b.WriteByte(first)

		zw := zlib.NewWriter(&b)
		_, _ = io.WriteString(zw, e.body)
		_ = zw.Close()
	}

	sum := sha1.Sum(b.Bytes()) // #nosec G401 -- the pack trailer is defined as SHA-1
	b.Write(sum[:])
	return b.Bytes()
}

// ensure the plumbing types stay referenced even as tests come and go
var _ = git.Ref(mainlineRef)
var _ = objTree
var _ = objBlob

// --- the hook's own rules, asserted by reason ------------------------------

// TestPreReceiveRefusesForTheRightReason closes a gap the end-to-end tests
// cannot.
//
// Over HTTP the client is told only "pre-receive hook declined": the detailed
// message travels on a sideband the raw requests above do not request. So those
// tests prove a refusal happened, not WHICH rule produced it — and a refusal by
// the wrong rule is how a control rots unnoticed. This one drives the hook
// directly and asserts the reason.
//
// The freshness case needs no quarantine to be realistic: with no
// GIT_OBJECT_DIRECTORY set, every object named is already in the store, which is
// precisely the shape of "you claimed what you did not send".
func TestPreReceiveRefusesForTheRightReason(t *testing.T) {
	_, bare, _, _, _ := writeWorld(t)
	bobSHA := strings.TrimSpace(mustGit(t, bare, "rev-parse", "--verify", bobRef))

	cases := []struct {
		name    string
		prefix  string
		stdin   string
		wantMsg string
	}{
		{
			name:    "a ref the credential does not name",
			prefix:  alicePrefix,
			stdin:   zeroOID + " " + bobSHA + " " + bobRef + "\n",
			wantMsg: "does not authorize the ref",
		},
		{
			name:    "two refs in one push",
			prefix:  alicePrefix,
			stdin:   zeroOID + " " + bobSHA + " " + aliceRef + "\n" + zeroOID + " " + bobSHA + " " + bobRef + "\n",
			wantMsg: "exactly one ref",
		},
		{
			name:    "deleting a ref",
			prefix:  alicePrefix,
			stdin:   bobSHA + " " + zeroOID + " " + aliceRef + "\n",
			wantMsg: "deleting a ref is not permitted",
		},
		{
			name:    "overwriting an existing audit ref",
			prefix:  alicePrefix,
			stdin:   bobSHA + " " + bobSHA + " " + aliceRef + "\n",
			wantMsg: "written once",
		},
		{
			name:    "claiming objects it did not send",
			prefix:  alicePrefix,
			stdin:   zeroOID + " " + bobSHA + " " + aliceRef + "\n",
			wantMsg: "did not send",
		},
		{
			name:    "a namespace prefix, even if the authorizer allowed it",
			prefix:  "refs/namespaces/runs/x/",
			stdin:   zeroOID + " " + bobSHA + " refs/namespaces/runs/x/refs/heads/run\n",
			wantMsg: "no usable authorization scope",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Chdir(bare)
			t.Setenv(gitd.EnvPushPrefix, c.prefix)
			t.Setenv("GIT_OBJECT_DIRECTORY", "")
			t.Setenv("GIT_ALTERNATE_OBJECT_DIRECTORIES", "")

			var stderr bytes.Buffer
			code := gitd.PreReceive(t.Context(), strings.NewReader(c.stdin), &stderr)

			if code == 0 {
				t.Fatalf("accepted, want refusal:\n%s", stderr.String())
			}
			if !strings.Contains(stderr.String(), c.wantMsg) {
				t.Errorf("refused for the wrong reason.\n got: %s\nwant to contain: %q",
					strings.TrimSpace(stderr.String()), c.wantMsg)
			}
		})
	}
}

// TestPreReceiveWithoutScopeRefuses: reaching the hook by any path that does not
// set the scope is, by itself, a reason to refuse.
func TestPreReceiveWithoutScopeRefuses(t *testing.T) {
	_, bare, _, _, _ := writeWorld(t)
	t.Chdir(bare)
	t.Setenv(gitd.EnvPushPrefix, "")

	var stderr bytes.Buffer
	if code := gitd.PreReceive(t.Context(), strings.NewReader(zeroOID+" "+zeroOID+" "+aliceRef+"\n"), &stderr); code == 0 {
		t.Fatal("a push with no authorization scope was accepted")
	}
	if !strings.Contains(stderr.String(), "no usable authorization scope") {
		t.Errorf("unexpected reason: %s", stderr.String())
	}
}

// --- the mandatory secret scan (§5.3) --------------------------------------

// leakedToken is shaped like a GitHub personal access token and is not one.
//
// Chosen after measuring rather than by intuition: gitleaks 8.30 does not report
// a bare AKIA-prefixed AWS key, and allowlists Amazon's documentation example
// outright. A test written around those fails while the scanner works — see the
// note on fakePAT in the secrets package.
const leakedToken = "ghp_016C7ce0e4bA8Fc4b1a7fD9C0f8E5d3B2a1908"

// TestSecretInAPushIsRefused: §5.3, "obligatoire côté serveur avant
// persistance".
//
// It also pins the rule that is easiest to break by accident: the refusal names
// the rule, the file and the line, and NEVER the value. A scanner that echoes
// what it finds has moved the leak into the pusher's terminal and the server's
// logs rather than stopped it.
func TestSecretInAPushIsRefused(t *testing.T) {
	root, bare, alice, _, _ := writeWorld(t)
	url := servePush(t, root, aliceScope())

	if err := os.WriteFile(filepath.Join(alice, "group_vars_all.yml"),
		[]byte("github_token: "+leakedToken+"\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	mustGit(t, alice, "add", ".")
	mustGit(t, alice, "commit", "-m", "a run that leaks")
	sha := strings.TrimSpace(mustGit(t, alice, "rev-parse", "HEAD"))

	out, err := gitWithToken(t, alice, pushToken, "push", url+"/depot.git", sha+":"+aliceRef)
	if err == nil {
		t.Fatalf("a push carrying a credential was accepted:\n%s", out)
	}
	if refExists(t, bare, aliceRef) {
		t.Fatal("the audit ref was created despite the secret")
	}

	if strings.Contains(out, leakedToken) {
		t.Errorf("THE REFUSAL ECHOED THE SECRET back to the client:\n%s", out)
	}
	if !strings.Contains(out, "group_vars_all.yml") {
		t.Errorf("the refusal does not say which file to look in:\n%s", out)
	}
}

// TestSecretAlreadyInTheMainlineDoesNotBlock is the test that proves the scan's
// PERIMETER, and it is the reason the perimeter is what it is.
//
// The scan covers the objects a push ADDS, not everything reachable. Were it the
// whole tree, one credential committed to the protected branch — long before
// warden existed — would refuse every run in that repository for ever, and the
// only escape would be rewriting the mainline's history.
func TestSecretAlreadyInTheMainlineDoesNotBlock(t *testing.T) {
	root := t.TempDir()
	bare := filepath.Join(root, "depot.git")
	mustGit(t, root, "init", "--bare", "--initial-branch=main", bare)

	seed := t.TempDir()
	mustGit(t, seed, "init", "--initial-branch=main", ".")
	if err := os.WriteFile(filepath.Join(seed, "legacy.yml"),
		[]byte("github_token: "+leakedToken+"\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	mustGit(t, seed, "add", ".")
	mustGit(t, seed, "commit", "-m", "a credential that predates the product")
	mainSHA := strings.TrimSpace(mustGit(t, seed, "rev-parse", "HEAD"))
	mustGit(t, seed, "push", bare, mainSHA+":"+mainlineRef)

	alice := t.TempDir()
	mustGit(t, alice, "init", "--initial-branch=main", ".")
	mustGit(t, alice, "fetch", bare, mainlineRef)
	mustGit(t, alice, "reset", "--hard", "FETCH_HEAD")

	url := servePush(t, root, aliceScope())

	// Alice adds something innocuous on top. The old credential is still
	// reachable from her commit — and must not be looked at again.
	if err := os.WriteFile(filepath.Join(alice, "inventory.ini"), []byte("[dev]\nlocalhost"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	mustGit(t, alice, "add", ".")
	mustGit(t, alice, "commit", "-m", "an honest run")
	sha := strings.TrimSpace(mustGit(t, alice, "rev-parse", "HEAD"))

	if out, err := gitWithToken(t, alice, pushToken, "push", url+"/depot.git", sha+":"+aliceRef); err != nil {
		t.Fatalf("a pre-existing mainline secret blocked an honest run — the scan "+
			"perimeter is the whole tree instead of the new objects: %v\n%s", err, out)
	}
}

// TestSecretInACommitMessageIsRefused closes the hole with a well-known shape.
//
// A token pasted into a commit message is exactly as leaked as one in a file,
// and scanning blobs alone would miss it while looking thorough.
func TestSecretInACommitMessageIsRefused(t *testing.T) {
	root, bare, alice, _, _ := writeWorld(t)
	url := servePush(t, root, aliceScope())

	mustGit(t, alice, "commit", "--allow-empty", "-m", "ran with token "+leakedToken)
	sha := strings.TrimSpace(mustGit(t, alice, "rev-parse", "HEAD"))

	out, err := gitWithToken(t, alice, pushToken, "push", url+"/depot.git", sha+":"+aliceRef)
	if err == nil {
		t.Fatalf("a credential in a commit message was accepted:\n%s", out)
	}
	if refExists(t, bare, aliceRef) {
		t.Fatal("the audit ref was created despite the secret in the message")
	}
	if strings.Contains(out, leakedToken) {
		t.Errorf("the refusal echoed the secret:\n%s", out)
	}
}

// --- the grant authorizer, end to end --------------------------------------

// TestGrantAuthorizerDrivesARealPush wires the signed grant through the whole
// write path.
//
// Until now every write test used a hand-made authorizer. This one mints a token
// the way the `api` role will, hands it to a real git client, and lets the
// transport verify it — so the format, the signature, the scope conversion and
// the hook all meet on the same push.
func TestGrantAuthorizerDrivesARealPush(t *testing.T) {
	root, bare, alice, _, _ := writeWorld(t)

	authz, err := gitd.NewGrantAuthorizer(jetonDeTest)
	if err != nil {
		t.Fatalf("NewGrantAuthorizer: %v", err)
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("locating the test binary: %v", err)
	}
	s, err := gitd.New(gitd.Config{
		Racine:         root,
		Jeton:          jetonDeTest,
		PushAuthorizer: authz,
		HooksDir:       filepath.Join(t.TempDir(), "hooks"),
		HookExecutable: exe,
	})
	if err != nil {
		t.Fatalf("gitd.New: %v", err)
	}
	srv := httptest.NewServer(s.PushHandler())
	t.Cleanup(srv.Close)

	grant, err := gitwire.SignPushGrant(gitwire.PushGrant{
		Repo:      "depot",
		RefPrefix: alicePrefix,
		Expires:   time.Now().Add(time.Hour).Unix(),
	}, jetonDeTest)
	if err != nil {
		t.Fatalf("SignPushGrant: %v", err)
	}

	if err := os.WriteFile(filepath.Join(alice, "inventory.ini"), []byte("[dev]\nlocalhost"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	mustGit(t, alice, "add", ".")
	mustGit(t, alice, "commit", "-m", "a run authorized by a signed grant")
	sha := strings.TrimSpace(mustGit(t, alice, "rev-parse", "HEAD"))

	if out, err := gitWithToken(t, alice, grant, "push", srv.URL+"/depot.git", sha+":"+aliceRef); err != nil {
		t.Fatalf("a correctly signed grant was refused: %v\n%s", err, out)
	}
	if !refExists(t, bare, aliceRef) {
		t.Fatal("the ref was not created")
	}

	// The same client, aiming at Bob's ref with the grant it holds. The scope is
	// signed, so it cannot be widened by the party that carries it.
	mustGit(t, alice, "commit", "--allow-empty", "-m", "aiming elsewhere")
	other := strings.TrimSpace(mustGit(t, alice, "rev-parse", "HEAD"))
	if out, err := gitWithToken(t, alice, grant, "push", srv.URL+"/depot.git", other+":"+bobRef); err == nil {
		t.Fatalf("the grant was used to write outside its scope:\n%s", out)
	}
}

// TestAForgedGrantIsRefused: the token travels through the user's machine, so
// the only thing standing between a curious user and any ref in the
// organisation is the signature.
func TestAForgedGrantIsRefused(t *testing.T) {
	root, bare, alice, _, _ := writeWorld(t)

	authz, err := gitd.NewGrantAuthorizer(jetonDeTest)
	if err != nil {
		t.Fatalf("NewGrantAuthorizer: %v", err)
	}
	exe, _ := os.Executable()
	s, err := gitd.New(gitd.Config{
		Racine:         root,
		Jeton:          jetonDeTest,
		PushAuthorizer: authz,
		HooksDir:       filepath.Join(t.TempDir(), "hooks"),
		HookExecutable: exe,
	})
	if err != nil {
		t.Fatalf("gitd.New: %v", err)
	}
	srv := httptest.NewServer(s.PushHandler())
	t.Cleanup(srv.Close)

	// Signed with a secret the attacker chose, naming a scope they wish they had.
	forged, err := gitwire.SignPushGrant(gitwire.PushGrant{
		Repo:      "depot",
		RefPrefix: "refs/audit/bob/",
		Expires:   time.Now().Add(time.Hour).Unix(),
	}, "a-secret-the-attacker-picked")
	if err != nil {
		t.Fatalf("SignPushGrant: %v", err)
	}

	mustGit(t, alice, "commit", "--allow-empty", "-m", "forged")
	sha := strings.TrimSpace(mustGit(t, alice, "rev-parse", "HEAD"))

	out, err := gitWithToken(t, alice, forged, "push", srv.URL+"/depot.git", sha+":"+bobRef)
	if err == nil {
		t.Fatalf("a forged grant was accepted:\n%s", out)
	}
	before := strings.TrimSpace(mustGit(t, bare, "rev-parse", "--verify", bobRef))
	if before == sha {
		t.Fatal("bob's audit ref was overwritten with a forged grant")
	}
}

// TestBuildingOnAnythingVisibleIsAccepted pins the invariant that broke first,
// and would break silently again.
//
// Git NEGOTIATES: a client does not resend objects the server advertised as
// having. So the set excluded by the freshness control must match exactly what
// receive.hideRefs leaves visible. Any ref that is advertised but not excluded
// looks, to the check, like an object claimed and not sent — and the honest push
// is refused with a message about theft.
//
// The reasoning that got this wrong was "a smaller excluded set only makes the
// check stricter, and an honest pusher holds its own history anyway". True about
// holding it, false about sending it.
//
// This exercises the third glob: the pusher's own audit namespace, which is
// un-hidden so that they can write in it, and therefore also visible to
// negotiation.
func TestBuildingOnAnythingVisibleIsAccepted(t *testing.T) {
	root, bare, alice, _, _ := writeWorld(t)
	url := servePush(t, root, aliceScope())

	// A first run, which lands in alice's namespace and becomes visible to her.
	if err := os.WriteFile(filepath.Join(alice, "inventory.ini"), []byte("[dev]\nlocalhost"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	mustGit(t, alice, "add", ".")
	mustGit(t, alice, "commit", "-m", "first run")
	first := strings.TrimSpace(mustGit(t, alice, "rev-parse", "HEAD"))
	if out, err := gitWithToken(t, alice, pushToken, "push", url+"/depot.git", first+":"+aliceRef); err != nil {
		t.Fatalf("first push: %v\n%s", err, out)
	}

	// A second run on top of it. Its parent is now on the server, so git will not
	// resend those objects — which is exactly the situation the excluded set has
	// to account for.
	if err := os.WriteFile(filepath.Join(alice, "inventory.ini"), []byte("[dev]\nlocalhost\nweb-01"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	mustGit(t, alice, "add", ".")
	mustGit(t, alice, "commit", "-m", "second run")
	second := strings.TrimSpace(mustGit(t, alice, "rev-parse", "HEAD"))

	const secondRef = "refs/audit/alice/01JZQ0000000000000SECND"
	out, err := gitWithToken(t, alice, pushToken, "push", url+"/depot.git", second+":"+secondRef)
	if err != nil {
		t.Fatalf("a run built on the pusher's own previous audit commit was refused — "+
			"the visible set and receive.hideRefs have diverged: %v\n%s", err, out)
	}
	if !refExists(t, bare, secondRef) {
		t.Error("the second audit ref was not created")
	}
}

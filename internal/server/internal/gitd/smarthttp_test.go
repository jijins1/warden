// Stage 1 of ADR-0010: temporary bare repositories, httptest.Server, a real git
// client. No database, no container, no cluster.
//
// The client is the git binary rather than a hand-built HTTP request wherever it
// can be. What ADR-0007 asks for is not "the server returns the right body" but
// "a runner negotiating normally cannot reach another run" — and that is only
// visible when git does the negotiating.

package gitd_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jijins1/warden/internal/git"
	"github.com/jijins1/warden/internal/server/internal/gitd"
	"github.com/jijins1/warden/internal/server/internal/gitwire"
)

const (
	namespaceA = "runs/01JZQ0000000000000000RUNA"
	namespaceB = "runs/01JZQ0000000000000000RUNB"

	tokenA = "token-of-run-a"
	tokenB = "token-of-run-b"
)

// tableAuthorizer is a table-backed implementation of the port.
//
// It stands in for whatever the next lot wires onto real state. What these tests
// check does not depend on where a scope comes from, only on what the transport
// does with it — which is exactly what the port separates.
type tableAuthorizer map[string]gitd.Scope

func (a tableAuthorizer) Authorize(_ context.Context, token string) (gitd.Scope, error) {
	s, ok := a[token]
	if !ok {
		return gitd.Scope{}, errors.New("unknown token")
	}
	return s, nil
}

// gitBinary returns the path to git, or skips the test.
func gitBinary(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git not in PATH: %v", err)
	}
	return path
}

// runGit executes git in dir with a neutralised environment.
//
// The same hardening internal/git applies to its own invocations: the config of
// whoever runs the tests must not decide the outcome.
func runGit(t *testing.T, dir string, args ...string) (string, error) {
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
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := runGit(t, dir, args...)
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// twoRuns builds one bare repository holding one run per namespace.
//
// This is the situation ADR-0007 describes: both runs live in the SAME
// repository, and nothing but GIT_NAMESPACE separates them.
func twoRuns(t *testing.T) (root, shaA, shaB string) {
	t.Helper()

	root = t.TempDir()
	bare := filepath.Join(root, "depot.git")
	mustGit(t, root, "init", "--bare", "--initial-branch=main", bare)

	src := t.TempDir()
	mustGit(t, src, "init", "--initial-branch=main", ".")

	commit := func(content string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(src, "file.txt"), []byte(content), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		mustGit(t, src, "add", "file.txt")
		mustGit(t, src, "commit", "-m", content)
		return strings.TrimSpace(mustGit(t, src, "rev-parse", "HEAD"))
	}

	shaA = commit("content-of-run-a")
	mustGit(t, src, "push", bare, shaA+":"+namespacedRef(namespaceA, "refs/heads/run"))

	shaB = commit("content-of-run-b")
	mustGit(t, src, "push", bare, shaB+":"+namespacedRef(namespaceB, "refs/heads/run"))

	return root, shaA, shaB
}

// namespacedRef builds the real ref path a namespace maps to.
//
// Worth spelling out, because it is not the path one would guess and getting it
// wrong produces an empty advertisement rather than an error: git NESTS every
// slash-separated component of GIT_NAMESPACE. A namespace of "a/b" resolves to
// refs/namespaces/a/refs/namespaces/b/, not refs/namespaces/a/b/.
func namespacedRef(ns, ref string) string {
	var b strings.Builder
	for _, component := range strings.Split(ns, "/") {
		b.WriteString("refs/namespaces/")
		b.WriteString(component)
		b.WriteString("/")
	}
	b.WriteString(ref)
	return b.String()
}

// serveSmart serves the smart HTTP handler on an httptest.Server.
func serveSmart(t *testing.T, root string, authz gitd.Authorizer) string {
	t.Helper()
	gitBinary(t) // skip early rather than on the first request

	s, err := gitd.New(gitd.Config{
		Racine:     root,
		Jeton:      jetonDeTest,
		Authorizer: authz,
	})
	if err != nil {
		t.Fatalf("gitd.New: %v", err)
	}

	srv := httptest.NewServer(s.SmartHandler())
	t.Cleanup(srv.Close)
	return srv.URL
}

// bothRuns gives each token its own namespace inside the same repository.
func bothRuns(t *testing.T) gitd.Authorizer {
	t.Helper()
	nsA, err := git.NewNamespace(namespaceA)
	if err != nil {
		t.Fatalf("namespace A: %v", err)
	}
	nsB, err := git.NewNamespace(namespaceB)
	if err != nil {
		t.Fatalf("namespace B: %v", err)
	}
	return tableAuthorizer{
		tokenA: {Repo: "depot", Namespace: nsA},
		tokenB: {Repo: "depot", Namespace: nsB},
	}
}

// gitWithToken drives a real git client authenticating with a bearer token.
//
// The token travels through http.extraHeader for the reason Git.WithAuth gives:
// a command line is readable by anyone on the machine through /proc.
func gitWithToken(t *testing.T, dir, token string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), gitBinary(t), append([]string{ // #nosec G204 -- literal test arguments
		"-c", "http.extraHeader=Authorization: Bearer " + token,
	}, args...)...)
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=/usr/bin:/bin",
		"HOME=" + dir,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"LC_ALL=C",
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestOneRunSeesOnlyItsOwnRef is THE negative test ADR-0007 demands by name:
// "un run tentant de lire la ref d'un autre run doit échouer".
//
// It goes through ls-remote, that is through the protocol's ref advertisement,
// because that is where the leak would happen — before a single `want` is sent.
// A runner that can see someone else's ref already has what it needs to ask for
// it.
func TestOneRunSeesOnlyItsOwnRef(t *testing.T) {
	root, shaA, shaB := twoRuns(t)
	base := serveSmart(t, root, bothRuns(t))
	client := t.TempDir()

	out, err := gitWithToken(t, client, tokenA, "ls-remote", base+"/depot.git")
	if err != nil {
		t.Fatalf("ls-remote with run A's token: %v\n%s", err, out)
	}

	if !strings.Contains(out, shaA) {
		t.Errorf("run A cannot see its own ref (%s):\n%s", shaA, out)
	}
	if strings.Contains(out, shaB) {
		t.Errorf("LEAK: run A sees run B's ref (%s):\n%s", shaB, out)
	}
	if strings.Contains(out, "refs/namespaces") {
		t.Errorf("the namespace shows through in the advertisement:\n%s", out)
	}
}

// TestExplicitWantOnAnotherRunFails closes the bypass that survives a clean
// advertisement.
//
// A SHA is not a secret: it leaks through an audit diff, a log, an error
// message. Were uploadpack.allowAnySHA1InWant enabled, knowing one would be
// enough to pull another run's tree without ever reading a ref — the whole
// namespace isolation bypassed from underneath.
func TestExplicitWantOnAnotherRunFails(t *testing.T) {
	root, _, shaB := twoRuns(t)
	base := serveSmart(t, root, bothRuns(t))

	client := t.TempDir()
	mustGit(t, client, "init", "--initial-branch=main", ".")

	out, err := gitWithToken(t, client, tokenA, "fetch", "--depth=1", base+"/depot.git", shaB)
	if err == nil {
		t.Fatalf("LEAK: run A pulled run B's object (%s):\n%s", shaB, out)
	}
}

// TestProtocolIsPinnedToV0 pins the least obvious control in this package, and
// the one whose loss would be silent.
//
// git's own documentation states that "namespaces on a server are not effective
// for read access control". Measured on git 2.43, the gap is precise: under
// protocol v2, uploadpack.allowAnySHA1InWant is not enforced, and a client that
// names an unadvertised object by its SHA receives it — namespace or not,
// configuration or not. Under v0 the same request is refused.
//
// The transport therefore strips the Git-Protocol header, so the client cannot
// select v2. That is a one-line control protecting the whole isolation
// property; a well-meaning cleanup would remove it without a single test going
// red. Hence this one, which asks explicitly for v2.
func TestProtocolIsPinnedToV0(t *testing.T) {
	root, _, shaB := twoRuns(t)
	base := serveSmart(t, root, bothRuns(t))

	for _, version := range []string{"0", "1", "2"} {
		t.Run("client asks for protocol v"+version, func(t *testing.T) {
			client := t.TempDir()
			mustGit(t, client, "init", "--initial-branch=main", ".")

			out, err := gitWithToken(t, client, tokenA,
				"-c", "protocol.version="+version,
				"fetch", base+"/depot.git", shaB)
			if err == nil {
				t.Fatalf("LEAK under protocol v%s: run B's object was served (%s):\n%s",
					version, shaB, out)
			}
			if !strings.Contains(out, "unadvertised object") {
				t.Errorf("refused, but not by the want check — the control may have moved:\n%s", out)
			}
		})
	}
}

// TestRepositoryComesFromTheTokenNotTheURL pins the design choice that carries
// the most security in this file.
//
// The repository path the client presents is DISCARDED. A runner therefore
// cannot name another repository, and is never told whether one exists — which
// removes enumeration along the way.
func TestRepositoryComesFromTheTokenNotTheURL(t *testing.T) {
	root, shaA, _ := twoRuns(t)
	base := serveSmart(t, root, bothRuns(t))
	client := t.TempDir()

	out, err := gitWithToken(t, client, tokenA, "ls-remote", base+"/a-repository-that-does-not-exist.git")
	if err != nil {
		t.Fatalf("ls-remote on a bogus path: %v\n%s", err, out)
	}
	if !strings.Contains(out, shaA) {
		t.Errorf("the URL path was followed instead of the token's repository:\n%s", out)
	}
}

// rawCall probes the service without git, for the cases git would refuse to
// formulate.
func rawCall(t *testing.T, method, url, token string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, url, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	return resp
}

// TestRefusedTokenTeachesNothing keeps the stance the internal API already
// takes. No body, no WWW-Authenticate, no difference between absent and wrong —
// the service is reachable from a hostile pod, and each of those differences
// informs whoever is probing.
func TestRefusedTokenTeachesNothing(t *testing.T) {
	root, _, _ := twoRuns(t)
	base := serveSmart(t, root, bothRuns(t))

	cases := []struct {
		name  string
		token string
	}{
		{"absent token", ""},
		{"unknown token", "a-token-that-does-not-exist"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp := rawCall(t, http.MethodGet, base+"/depot.git/info/refs?service=git-upload-pack", c.token)
			defer func() { _ = resp.Body.Close() }()

			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("status %d, want 401", resp.StatusCode)
			}
			if resp.Header.Get("WWW-Authenticate") != "" {
				t.Error("WWW-Authenticate present: it invites a retry and confirms the surface")
			}
			body, _ := io.ReadAll(resp.Body)
			if len(body) != 0 {
				t.Errorf("non-empty body: %q", body)
			}
		})
	}
}

// TestScopeWithoutNamespaceIsRefused: the zero value fails closed.
//
// An empty GIT_NAMESPACE gives git the FULL ref view. That is exactly the leak
// ADR-0007 exists to prevent, and here it would follow from a single omission in
// an implementation of the port. The transport refuses rather than trusting its
// caller.
func TestScopeWithoutNamespaceIsRefused(t *testing.T) {
	root, _, _ := twoRuns(t)
	authz := tableAuthorizer{"badly-built-token": {Repo: "depot"}}
	base := serveSmart(t, root, authz)

	resp := rawCall(t, http.MethodGet, base+"/depot.git/info/refs?service=git-upload-pack", "badly-built-token")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusOK {
		t.Fatal("a scope without a namespace served the advertisement: full ref view")
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status %d, want 403", resp.StatusCode)
	}
}

// TestReceivePackIsRefused bounds this slice.
//
// Writing is not served until per-REF authorization exists. A push authorized at
// repository level would let any token holder write over someone else's audit
// ref — a 403 beats a write path with no control on it.
func TestReceivePackIsRefused(t *testing.T) {
	root, _, _ := twoRuns(t)
	base := serveSmart(t, root, bothRuns(t))

	cases := []struct {
		name   string
		method string
		path   string
	}{
		{"advertisement of the write service", http.MethodGet, "/depot.git/info/refs?service=git-receive-pack"},
		{"receive-pack itself", http.MethodPost, "/depot.git/git-receive-pack"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp := rawCall(t, c.method, base+c.path, tokenA)
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("status %d, want 403", resp.StatusCode)
			}
		})
	}
}

// TestDumbProtocolIsRefused closes the quietest bypass of them all.
//
// A GET on an object path serves the repository's raw bytes and IGNORES
// GIT_NAMESPACE: the namespace is a property of the smart protocol, not of the
// filesystem. Serving the dumb protocol would publish every run of every user to
// anyone who can read a SHA.
func TestDumbProtocolIsRefused(t *testing.T) {
	root, shaB, _ := twoRuns(t)
	base := serveSmart(t, root, bothRuns(t))

	paths := []string{
		"/depot.git/objects/" + shaB[:2] + "/" + shaB[2:],
		"/depot.git/objects/info/packs",
		"/depot.git/info/refs", // no service parameter: that is the dumb form
		"/depot.git/HEAD",      // read by the dumb client to find the branch
		"/depot.git/objects/info/alternates",
	}

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			resp := rawCall(t, http.MethodGet, base+path, tokenA)
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("status %d, want 403", resp.StatusCode)
			}
		})
	}
}

// TestSmartAddrWithoutAuthorizerRefusesToStart: fail cold rather than in flight,
// as New already does for the token and the root.
//
// A listening address with no authorization port would serve the repository to
// whoever asks. That is not a degradation, it is an open surface.
func TestSmartAddrWithoutAuthorizerRefusesToStart(t *testing.T) {
	_, err := gitd.New(gitd.Config{
		Racine:    t.TempDir(),
		Jeton:     jetonDeTest,
		SmartAddr: ":0",
		// Authorizer deliberately absent.
	})
	if err == nil {
		t.Fatal("a smart HTTP address without an authorizer started")
	}
}

// TestWithoutSmartHTTPTheServiceStillWorks: the internal diff API does not
// depend on the smart HTTP, and a deployment serving only diffs stays valid.
func TestWithoutSmartHTTPTheServiceStillWorks(t *testing.T) {
	s, err := gitd.New(gitd.Config{Racine: t.TempDir(), Jeton: jetonDeTest})
	if err != nil {
		t.Fatalf("gitd.New without smart HTTP: %v", err)
	}
	if s.SmartHandler() != nil {
		t.Error("a smart HTTP handler is mounted although no authorizer was provided")
	}
}

// --- the run token, end to end ---------------------------------------------

// TestFetchAuthorizerIsolatesRunsForReal closes the last gap of ADR-0007.
//
// The read transport shipped in ADR-0026 against an Authorizer that had no
// implementation, so it was never actually served: every isolation test until now
// used a hand-made table. This one mints the token the way the control plane
// will, and lets the transport verify it — signature, namespace conversion and
// ref confinement meeting on one real fetch.
func TestFetchAuthorizerIsolatesRunsForReal(t *testing.T) {
	root, shaA, shaB := twoRuns(t)

	authz, err := gitd.NewFetchAuthorizer(jetonDeTest)
	if err != nil {
		t.Fatalf("NewFetchAuthorizer: %v", err)
	}
	s, err := gitd.New(gitd.Config{Racine: root, Jeton: jetonDeTest, Authorizer: authz})
	if err != nil {
		t.Fatalf("gitd.New: %v", err)
	}
	srv := httptest.NewServer(s.SmartHandler())
	t.Cleanup(srv.Close)

	grant, err := gitwire.SignFetchGrant(gitwire.FetchGrant{
		Repo:      "depot",
		Namespace: namespaceA,
		Expires:   time.Now().Add(time.Hour).Unix(),
	}, jetonDeTest)
	if err != nil {
		t.Fatalf("SignFetchGrant: %v", err)
	}

	client := t.TempDir()
	out, err := gitWithToken(t, client, grant, "ls-remote", srv.URL+"/depot.git")
	if err != nil {
		t.Fatalf("ls-remote with a signed run token: %v\n%s", err, out)
	}
	if !strings.Contains(out, shaA) {
		t.Errorf("run A cannot see its own ref:\n%s", out)
	}
	if strings.Contains(out, shaB) {
		t.Errorf("LEAK: run A sees run B's ref:\n%s", out)
	}
}

// TestAPushGrantIsNotAFetchGrant pins the domain separation.
//
// One secret now signs three things: internal API authentication, write
// authorization, and read authorization. The bearer of a read grant is a runner
// pod — arbitrary code, by §2. If the kinds were interchangeable it could present
// its token to the write transport, and the whole conjunction of ADR-0027 would
// rest on a prefix nobody checks.
func TestAPushGrantIsNotAFetchGrant(t *testing.T) {
	root, _, _ := twoRuns(t)

	fetchAuthz, err := gitd.NewFetchAuthorizer(jetonDeTest)
	if err != nil {
		t.Fatalf("NewFetchAuthorizer: %v", err)
	}
	s, err := gitd.New(gitd.Config{Racine: root, Jeton: jetonDeTest, Authorizer: fetchAuthz})
	if err != nil {
		t.Fatalf("gitd.New: %v", err)
	}
	srv := httptest.NewServer(s.SmartHandler())
	t.Cleanup(srv.Close)

	// A perfectly valid PUSH grant, signed with the real secret.
	push, err := gitwire.SignPushGrant(gitwire.PushGrant{
		Repo:      "depot",
		RefPrefix: "refs/audit/1/",
		Expires:   time.Now().Add(time.Hour).Unix(),
	}, jetonDeTest)
	if err != nil {
		t.Fatalf("SignPushGrant: %v", err)
	}

	resp := rawCall(t, http.MethodGet,
		srv.URL+"/depot.git/info/refs?service=git-upload-pack", push)
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a push grant was accepted on the read transport: status %d", resp.StatusCode)
	}

	// And the reverse: swapping the kind prefix must not convert one into the
	// other, because the kind is inside the signature.
	_, reste, _ := strings.Cut(push, ".")
	if resp2 := rawCall(t, http.MethodGet,
		srv.URL+"/depot.git/info/refs?service=git-upload-pack", "wfg1."+reste); resp2.StatusCode != http.StatusUnauthorized {
		_ = resp2.Body.Close()
		t.Fatal("relabelling a push grant as a fetch grant was accepted")
	} else {
		_ = resp2.Body.Close()
	}
}

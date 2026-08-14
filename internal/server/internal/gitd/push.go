package gitd

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	// #nosec G504 -- httpoxy (CVE-2016-5386) was fixed in Go 1.6.3: net/http/cgi
	// no longer turns a `Proxy:` request header into HTTP_PROXY in the child.
	// This module requires Go 1.26.5, and serveCGI drops the header regardless.
	"net/http/cgi"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jijins1/warden/internal/git"
)

// PushScope is what a push credential opens, and nothing more.
//
// Every field is mandatory and the zero value is refused by the transport, for
// the reason Scope gives: an omission in an implementation of the port must cost
// a 403, never an organisation's audit history.
type PushScope struct {
	// Repo is a repository identifier, resolved by the same validation the read
	// transport and the internal API use.
	Repo string

	// Ref is THE single ref this credential may CREATE. Not a prefix, not a
	// pattern: the exact name, checked again in the hook.
	Ref git.Ref

	// Mainline is the only ref the pusher may build upon — the server's mirror of
	// the protected branch. It anchors the freshness control of ADR-0027, which
	// is why it must never be derived from anything the client sent.
	Mainline git.Ref
}

// PushAuthorizer resolves a presented token into the push scope it opens.
//
// A port SEPARATE from Authorizer, deliberately, and the separation is a security
// control rather than tidiness. Folding write permission into Scope as a boolean
// would make "a runner cannot write" a property of an implementation, which a
// miscomputed field reopens. Kept apart, it is a property of the code path: the
// transport served to runners never consults this port, and this one is not even
// on the same TCP port (ADR-0027).
type PushAuthorizer interface {
	AuthorizePush(ctx context.Context, token string) (PushScope, error)
}

// allowedPush is the exhaustive list of what the write transport serves.
//
// Two entries, mirroring the read transport's allowlist and closing the same
// families: the dumb protocol, and anything that is not receive-pack.
var allowedPush = []service{
	{method: http.MethodGet, suffix: "info/refs", query: "service=git-receive-pack"},
	{method: http.MethodPost, suffix: "git-receive-pack"},
}

// DefaultMaxPushBytes bounds a push body.
//
// Generous on purpose. ADR-0003 accepts that the first push of a divergent local
// branch carries unbounded history, and the simplified git model of ADR-0027 made
// that the NORMAL case rather than an edge one — a tight bound here would refuse
// ordinary work. The real protection is that this is an authenticated,
// per-credential surface, not an anonymous one.
const DefaultMaxPushBytes = 1 << 30

// smartPush serves git-receive-pack to the CLI.
type smartPush struct {
	root     string
	gitPath  string
	hooksDir string
	maxBytes int64
	repos    depots
	authz    PushAuthorizer
	logger   *slog.Logger
}

func newSmartPush(root, hooksDir string, maxBytes int64, authz PushAuthorizer, logger *slog.Logger) (*smartPush, error) {
	path, err := exec.LookPath("git")
	if err != nil {
		return nil, fmt.Errorf("git binary not found, the write transport cannot be served: %w", err)
	}
	if logger == nil {
		logger = slog.Default()
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxPushBytes
	}
	return &smartPush{
		root:     root,
		gitPath:  path,
		hooksDir: hooksDir,
		maxBytes: maxBytes,
		repos:    depots{racine: root},
		authz:    authz,
		logger:   logger,
	}, nil
}

// ServeHTTP authenticates, authorizes, then hands the request to git.
func (h *smartPush) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	token, ok := bearer(r)
	if !ok {
		refuse(w)
		return
	}

	scope, err := h.authz.AuthorizePush(r.Context(), token)
	if err != nil {
		refuse(w)
		return
	}

	if err := validPushScope(scope); err != nil {
		h.logger.Error("the push authorizer returned an unusable scope; refusing",
			slog.String("reason", err.Error()))
		w.WriteHeader(http.StatusForbidden)
		return
	}

	svc, ok := matchIn(allowedPush, r)
	if !ok {
		w.WriteHeader(http.StatusForbidden)
		return
	}

	dir, wireErr := h.repos.dir(scope.Repo)
	if wireErr != nil {
		h.logger.Error("push scope names a repository that cannot be resolved",
			slog.String("repo", scope.Repo), slog.String("code", string(wireErr.Code)))
		w.WriteHeader(http.StatusForbidden)
		return
	}

	if r.ContentLength > h.maxBytes {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}

	h.serveCGI(w, r, svc, dir, scope)
}

// validPushScope rejects a scope no implementation should have produced.
//
// The checks are redundant with what a correct authorizer does, and that is the
// point: this is the last place the product can refuse before git is handed a
// repository and a hook.
func validPushScope(s PushScope) error {
	if s.Repo == "" {
		return fmt.Errorf("no repository")
	}
	if s.Ref == "" {
		return fmt.Errorf("no ref")
	}
	if _, err := git.NewRef(string(s.Ref)); err != nil {
		return fmt.Errorf("malformed ref")
	}
	// A namespace ref is how a run reaches its own code (ADR-0007). A client that
	// could create one could make the server believe it holds an object it
	// covets — the first attack vector of gitnamespaces(7). Never writable.
	if strings.HasPrefix(string(s.Ref), "refs/namespaces/") {
		return fmt.Errorf("refs/namespaces/ is not writable by a client")
	}
	if s.Mainline != "" {
		if _, err := git.NewRef(string(s.Mainline)); err != nil {
			return fmt.Errorf("malformed mainline ref")
		}
	}
	return nil
}

func (h *smartPush) serveCGI(w http.ResponseWriter, r *http.Request, svc service, dir string, scope PushScope) {
	pathInfo := "/" + filepath.Base(dir) + "/" + svc.suffix

	inner := r.Clone(r.Context())
	inner.URL = &url.URL{Path: pathInfo, RawQuery: svc.query}
	inner.RequestURI = ""
	// Same pinning as the read transport, for consistency of the surface rather
	// than for a measured gap: probe A4 found hideRefs behaving identically under
	// both versions. One protocol on both edges is one protocol to reason about.
	inner.Header.Del("Git-Protocol")
	inner.Header.Del("Proxy")
	if r.Body != nil {
		inner.Body = http.MaxBytesReader(w, r.Body, h.maxBytes)
	}

	backend := &cgi.Handler{
		Path: h.gitPath,
		Args: []string{"http-backend"},
		Dir:  h.root,
		Env:  h.env(scope),
	}
	backend.ServeHTTP(w, inner)
}

// env builds the CGI environment for a write request.
func (h *smartPush) env(scope PushScope) []string {
	env := []string{
		"PATH=/usr/bin:/bin",
		"LC_ALL=C",
		"GIT_PROJECT_ROOT=" + h.root,
		"GIT_HTTP_EXPORT_ALL=1",
		"HOME=" + h.root,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",

		// Writing the reflog needs an identity, and HOME is neutralised, so
		// without these the ref update fails for a reason that has nothing to do
		// with the push.
		"GIT_COMMITTER_NAME=warden",
		"GIT_COMMITTER_EMAIL=warden@invalid",

		// The scope, carried to the hook process. See the comment on EnvPushRef:
		// these names cannot be forged from a request header, and they are set
		// unconditionally rather than passed through.
		EnvPushRef + "=" + string(scope.Ref),
		EnvPushMainline + "=" + string(scope.Mainline),
	}

	overrides := [][2]string{
		// http-backend refuses receive-pack outright unless this is on.
		{"http.receivepack", "true"},

		// The one place in the product where hooks are NOT neutralised, and the
		// exception is bounded by construction: the path is absolute, owned by
		// the role, outside the repository root, and set here through the
		// environment — so a hooks/ directory inside a bare repository is never
		// consulted, whatever it contains.
		{"core.hooksPath", h.hooksDir},

		// Hide what the pusher has no business seeing. Measured (ADR-0027, probe
		// A4): hideRefs is a lock and not merely a curtain — it refuses updates to
		// a hidden ref as well as omitting it from the advertisement, identically
		// under both protocol versions. That makes it a real second line behind
		// the hook, which remains the control.
		{"receive.hideRefs", "refs/audit/"},
		{"receive.hideRefs", "refs/namespaces/"},
		// …and un-hide exactly the one ref this credential may write.
		//
		// Necessary, and the reason is the measurement itself: hideRefs is a LOCK,
		// not a curtain (probe A4). Hiding refs/audit/ wholesale therefore denies
		// the pusher their OWN ref — "deny updating a hidden ref" — and the honest
		// push fails.
		//
		// The subtler damage is what that does to the test suite: with the whole
		// prefix hidden, the attack tests pass because git refuses first, and the
		// freshness control is never reached. A green suite, a false one. The
		// negation is what puts the control back on the path it is supposed to
		// guard.
		{"receive.hideRefs", "!" + string(scope.Ref)},

		{"receive.denyDeletes", "true"},
		{"receive.denyNonFastForwards", "true"},

		// Push options are a channel from the client into the hook. There is
		// nothing the hook should learn from the client.
		{"receive.advertisePushOptions", "false"},

		// Bound the pack before it is read whole, rather than after.
		{"receive.maxInputSize", strconv.FormatInt(h.maxBytes, 10)},

		// ADR-0004 reserves maintenance for the scheduled path, which holds the
		// per-repository lock. An automatic gc firing under a push would race it.
		{"gc.auto", "0"},

		{"core.logAllRefUpdates", "true"},
	}

	env = append(env, "GIT_CONFIG_COUNT="+strconv.Itoa(len(overrides)))
	for i, kv := range overrides {
		env = append(env,
			"GIT_CONFIG_KEY_"+strconv.Itoa(i)+"="+kv[0],
			"GIT_CONFIG_VALUE_"+strconv.Itoa(i)+"="+kv[1],
		)
	}
	return env
}

// prepareHooks lays out the hook directory the write transport points git at.
//
// The hook is this binary, reached through a symlink named pre-receive, and
// dispatched on argv[0]. No shell script: that would assume an interpreter exists
// in the image and would put quoting between us and a security control.
func prepareHooks(dir, executable string) error {
	if executable == "" {
		exe, err := os.Executable()
		if err != nil {
			return fmt.Errorf("locating our own binary for the pre-receive hook: %w", err)
		}
		executable = exe
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("creating the hook directory: %w", err)
	}

	link := filepath.Join(dir, "pre-receive")
	// Replace rather than reuse: a stale link left by a previous version would
	// silently run the wrong binary.
	if err := os.Remove(link); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("clearing the previous pre-receive hook: %w", err)
	}
	if err := os.Symlink(executable, link); err != nil {
		return fmt.Errorf("installing the pre-receive hook: %w", err)
	}
	return nil
}

package gitd

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	// #nosec G504 -- httpoxy (CVE-2016-5386) is the reason for the blocklist, and
	// it was fixed in Go 1.6.3: net/http/cgi no longer turns a `Proxy:` request
	// header into HTTP_PROXY in the child. This module requires Go 1.26.5, and
	// serveCGI drops the header anyway rather than relying on that alone.
	"net/http/cgi"
	"net/url"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jijins1/warden/internal/git"
)

// Scope is what a presented credential opens, and nothing more.
//
// Both fields are mandatory and the zero value is refused by the transport. An
// empty Namespace is not a shortcut for "no restriction": it gives git the full
// ref view of the repository, which is precisely the leak ADR-0007 exists to
// prevent. Making the zero value fail closed means an omission in an
// implementation of Authorizer costs a 403 rather than every run in the
// organisation.
type Scope struct {
	// Repo is a repository identifier, resolved through the same validation the
	// internal API uses.
	Repo string

	// Namespace confines the ref view to one run (ADR-0007):
	// refs/namespaces/<ns>/.
	Namespace git.Namespace
}

// Authorizer resolves a presented token into the scope it opens.
//
// This is a PORT, deliberately without an implementation in this package. The
// run token of ADR-0007 does not exist anywhere yet — migration 00003 states
// that it never carries "le jeton git de l'ADR-0007" — and where its state lives
// is a decision with consequences: `gitd` does not talk to PostgreSQL, and that
// is deliberate (see doc.go). Defining the edge here lets the transport and its
// isolation property be built and tested now, without pretending that question
// is settled.
//
// Implementations are reached from a hostile domain. They should treat an
// unknown token and an expired one identically, and must not let the duration of
// a lookup distinguish them.
type Authorizer interface {
	Authorize(ctx context.Context, token string) (Scope, error)
}

// service is one of the two smart HTTP endpoints this transport serves.
type service struct {
	method string
	// suffix is the tail of the request path, rebuilt verbatim into PATH_INFO.
	// It is a literal of ours, never a fragment of what the client sent.
	suffix string
	// query is the exact query string required, empty meaning none.
	query string
}

// allowed is the exhaustive list of what the transport serves.
//
// An allowlist rather than a denylist, and short on purpose. Everything absent
// from it is refused, which closes two families at once:
//
//   - `service=git-receive-pack`, and the receive-pack endpoint itself. Writing
//     is out of scope until per-ref authorization exists (see doc.go): a push
//     authorized at repository level would let any token holder write over
//     someone else's audit ref.
//   - THE DUMB PROTOCOL — objects/xx/…, objects/info/packs, a bare info/refs,
//     HEAD. Those paths serve the repository's bytes straight off the disk and
//     IGNORE GIT_NAMESPACE, because the namespace is a property of the smart
//     protocol and not of the filesystem. Leaving them open would publish every
//     run of every user to anyone able to read a SHA, with the ref advertisement
//     still looking perfectly clean.
var allowed = []service{
	{method: http.MethodGet, suffix: "info/refs", query: "service=git-upload-pack"},
	{method: http.MethodPost, suffix: "git-upload-pack"},
}

// maxUploadPackBody bounds the request body of an upload-pack negotiation.
//
// A negotiation carries want/have lines: a few kilobytes for the single-ref view
// a namespace exposes. The bound exists because this endpoint is reachable from
// arbitrary code, and an unbounded body is a way to spend the single git
// instance's memory without authenticating anything.
const maxUploadPackBody = 1 << 20

// smartHTTP serves receive-pack's read-only counterpart to the runners.
type smartHTTP struct {
	root    string
	gitPath string // resolved once: cgi.Handler runs it directly, without a PATH lookup
	repos   depots
	authz   Authorizer
	logger  *slog.Logger
}

// newSmartHTTP resolves the git binary and builds the transport.
//
// The lookup happens here rather than on the first request so that a deployment
// without git fails at startup. It is NOT the version check of ADR-0005, which
// belongs to the process and stays in the assembler — this only answers "is
// there a binary to exec", a question cgi.Handler would otherwise ask under a
// request from a hostile caller.
func newSmartHTTP(root string, authz Authorizer, logger *slog.Logger) (*smartHTTP, error) {
	path, err := exec.LookPath("git")
	if err != nil {
		return nil, fmt.Errorf("git binary not found, smart HTTP cannot be served: %w", err)
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &smartHTTP{
		root:    root,
		gitPath: path,
		repos:   depots{racine: root},
		authz:   authz,
		logger:  logger,
	}, nil
}

// ServeHTTP authenticates, authorizes, then hands the request to git.
func (h *smartHTTP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	token, ok := bearer(r)
	if !ok {
		refuse(w)
		return
	}

	scope, err := h.authz.Authorize(r.Context(), token)
	if err != nil {
		// Deliberately not logged at error level with the token in scope: this
		// is the expected outcome of every probe, and a service reachable from
		// arbitrary code must not let a caller fill the operator's logs.
		refuse(w)
		return
	}

	// The zero value fails closed. See Scope.
	if scope.Namespace == "" {
		h.logger.Error("authorizer returned a scope without a namespace; refusing",
			slog.String("repo", scope.Repo))
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if _, err := git.NewNamespace(scope.Namespace.String()); err != nil {
		h.logger.Error("authorizer returned an invalid namespace; refusing",
			slog.String("error", err.Error()))
		w.WriteHeader(http.StatusForbidden)
		return
	}

	svc, ok := match(r)
	if !ok {
		w.WriteHeader(http.StatusForbidden)
		return
	}

	// The repository comes from the SCOPE, never from the URL. This is the
	// control that matters most here: the path the client presented is dropped
	// on the floor, so a runner cannot name another repository, and is never
	// told whether one exists.
	dir, wireErr := h.repos.dir(scope.Repo)
	if wireErr != nil {
		h.logger.Error("scope names a repository that cannot be resolved",
			slog.String("repo", scope.Repo), slog.String("code", string(wireErr.Code)))
		// The caller learns nothing about the server's inventory: this is our
		// configuration error, not theirs.
		w.WriteHeader(http.StatusForbidden)
		return
	}

	if r.ContentLength > maxUploadPackBody {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}

	h.serveCGI(w, r, svc, dir, scope.Namespace)
}

// serveCGI runs git http-backend on a request whose path we rebuilt.
func (h *smartHTTP) serveCGI(w http.ResponseWriter, r *http.Request, svc service, dir string, ns git.Namespace) {
	// PATH_INFO is composed of two pieces we control: the directory name that
	// depots.dir validated, and one of the two literal suffixes in `allowed`.
	// Nothing the client sent survives into it.
	pathInfo := "/" + filepath.Base(dir) + "/" + svc.suffix

	inner := r.Clone(r.Context())
	inner.URL = &url.URL{Path: pathInfo, RawQuery: svc.query}
	inner.RequestURI = ""

	// Dropping Git-Protocol pins the wire protocol to v0, and it is a SECURITY
	// control rather than a compatibility choice.
	//
	// git http-backend derives GIT_PROTOCOL from this header, and with protocol
	// v2 the uploadpack.allow*InWant settings below are simply not enforced: a
	// client that names an unadvertised object by its SHA receives it, whatever
	// the namespace and whatever the configuration. Measured on git 2.43, and
	// the four combinations are pinned by TestProtocolIsPinnedToV0. ADR-0026
	// records the measurement and amends ADR-0007 accordingly.
	//
	// Removing the header server-side means the client cannot opt back in — it
	// may ask for v2 all it likes, http-backend never learns that it did.
	//
	// The cost is v0's less efficient negotiation, which is irrelevant here: the
	// runner fetches a single shallow ref (ADR-0007).
	inner.Header.Del("Git-Protocol")

	// Belt and braces against httpoxy: a caller-supplied `Proxy:` header must
	// never reach the child as HTTP_PROXY. Go stopped propagating it in 1.6.3,
	// so this is redundant today — and it costs one line to keep it true if the
	// request ever reaches the child by another route.
	inner.Header.Del("Proxy")
	if r.Body != nil {
		inner.Body = http.MaxBytesReader(w, r.Body, maxUploadPackBody)
	}

	backend := &cgi.Handler{
		Path: h.gitPath,
		Args: []string{"http-backend"},
		Dir:  h.root,
		Env:  h.env(ns),
	}
	backend.ServeHTTP(w, inner)
}

// env builds the CGI environment from scratch.
//
// Nothing is inherited: cgi.Handler passes only what is listed here, which is
// the same posture internal/git takes for every invocation it makes. The
// environment of the server process has no business reaching a child that serves
// a hostile caller.
func (h *smartHTTP) env(ns git.Namespace) []string {
	env := []string{
		"PATH=/usr/bin:/bin",
		"LC_ALL=C",

		"GIT_PROJECT_ROOT=" + h.root,

		// Safe only because PATH_INFO no longer comes from the client: the sole
		// repository reachable through it is the one the scope named.
		"GIT_HTTP_EXPORT_ALL=1",

		// The control this whole file exists to apply. Enforced by git, outside
		// the negotiation, exactly as ADR-0007 requires.
		"GIT_NAMESPACE=" + ns.String(),

		// HOME is set to the repository root rather than left unset: git falls
		// back to reading the passwd entry when it is absent, and an unreadable
		// one turns into a startup failure that looks like a repository fault.
		"HOME=" + h.root,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	}

	// GIT_CONFIG_COUNT is the environment-borne equivalent of `-c`, which
	// http-backend gives us no way to pass. Same mechanism as Git.WithAuth.
	//
	// The three allow*InWant keys are the reason this block exists. A namespace
	// hides refs, but it does not stop a client from asking for an object by its
	// SHA — and a SHA is not a secret, it leaks through an audit diff or a log.
	// Left enabled, any of these would let a runner pull another run's tree
	// without ever reading a ref, bypassing the isolation from underneath.
	// Setting them here also means a repository-local config cannot turn them
	// back on.
	overrides := [][2]string{
		{"core.hooksPath", "/dev/null"},
		{"uploadpack.allowAnySHA1InWant", "false"},
		{"uploadpack.allowReachableSHA1InWant", "false"},
		{"uploadpack.allowTipSHA1InWant", "false"},
		// Filters would let a client shape what it receives. The runner fetches
		// one shallow ref (ADR-0007) and has no use for them.
		{"uploadpack.allowFilter", "false"},
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

// match reports which allowed service the request names, if any.
//
// It matches on the TAIL of the path and ignores everything before it: the
// leading segments are the repository path, and that is the part the scope
// overrides. The suffix returned is the literal from `allowed`, never the
// client's bytes.
func match(r *http.Request) (service, bool) {
	path := strings.Trim(r.URL.Path, "/")
	for _, svc := range allowed {
		if r.Method != svc.method {
			continue
		}
		if path != svc.suffix && !strings.HasSuffix(path, "/"+svc.suffix) {
			continue
		}
		if r.URL.RawQuery != svc.query {
			continue
		}
		return svc, true
	}
	return service{}, false
}

// bearer extracts the token from the Authorization header.
func bearer(r *http.Request) (string, bool) {
	const prefix = "Bearer "
	raw := r.Header.Get("Authorization")
	if len(raw) <= len(prefix) || !strings.EqualFold(raw[:len(prefix)], prefix) {
		return "", false
	}
	return strings.TrimSpace(raw[len(prefix):]), true
}

// refuse denies without teaching the caller anything.
//
// No body, no WWW-Authenticate, no difference between a missing token and a
// wrong one — the same stance the internal API's middleware takes, and for the
// same reason: this service is reachable from arbitrary code. A
// WWW-Authenticate would also invite git to retry with a credential helper,
// which is noise on a path where the credential is injected deliberately.
func refuse(w http.ResponseWriter) {
	w.WriteHeader(http.StatusUnauthorized)
}

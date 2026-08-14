package gitd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jijins1/warden/internal/git"
	"github.com/jijins1/warden/internal/server/internal/gitd/secrets"
)

// Environment carrying the push scope from the transport down to the hook.
//
// The hook is a separate PROCESS, three exec boundaries away: CGI, then
// http-backend, then receive-pack. The environment is the only channel that
// crosses all three, and ADR-0027 measured that it does (probe A1b). The
// alternatives are worse — a file the hook reads is racy between concurrent
// pushes, and a call back into gitd would be a second authorization surface.
//
// The WARDEN_ prefix is not decoration. net/http/cgi turns every client request
// header into an HTTP_<NAME> variable, so anything the hook trusts must be
// impossible to forge from a header. These names cannot be produced that way, and
// the transport sets them unconditionally rather than passing them through.
const (
	EnvPushRef      = "WARDEN_PUSH_REF"
	EnvPushMainline = "WARDEN_PUSH_MAINLINE"
)

// zeroSHA is what git sends for "no value" on either side of an update.
const zeroSHA = "0000000000000000000000000000000000000000"

// update is one line of what receive-pack feeds the hook on stdin.
type update struct {
	old, new string
	ref      string
}

// PreReceive runs the hook and returns the process exit status.
//
// Non-zero means the push is refused, and git guarantees what that costs the
// attacker: the quarantine is destroyed and the object store is left untouched
// (ADR-0027, measured by probe A2). Everything this function decides therefore
// happens strictly before persistence, which is what §5.3 requires of the secret
// scan.
//
// Messages go to stderr, which git relays to the client prefixed "remote:". They
// are BUILT here, never assembled from server state: a message that echoed a ref
// name or a path found in the repository would hand a prober the very inventory
// the rest of this package hides.
func PreReceive(ctx context.Context, stdin io.Reader, stderr io.Writer) int {
	if err := preReceive(ctx, stdin); err != nil {
		// The write is best-effort: if the client has already hung up there is
		// nowhere to report to, and the refusal itself is what matters.
		_, _ = fmt.Fprintf(stderr, "warden: %s\n", err)
		return 1
	}
	return 0
}

func preReceive(ctx context.Context, stdin io.Reader) error {
	allowed, err := git.NewRef(os.Getenv(EnvPushRef))
	if err != nil {
		// The transport sets this on every request it lets through. Its absence
		// means the hook was reached by some other path, and the only safe
		// reading of that is refusal.
		return errors.New("this push carries no authorization scope")
	}

	updates, err := readUpdates(stdin)
	if err != nil {
		return err
	}

	// One ref per push, and it must be THE one. Not a convenience: an allowlist
	// that checked only "at least one update matches" would let a second,
	// unchecked update ride along in the same request.
	if len(updates) != 1 {
		return fmt.Errorf("a push must update exactly one ref, this one updates %d", len(updates))
	}
	u := updates[0]

	if u.ref != allowed.String() {
		return errors.New("this credential does not authorize the ref being pushed")
	}

	// Belt and braces over whatever the authorizer said. A namespace ref is how
	// a run reaches its own code (ADR-0007); letting a client create one is the
	// first attack vector of gitnamespaces(7), and it must be impossible here
	// even if an implementation of PushAuthorizer returns one.
	if strings.HasPrefix(allowed.String(), "refs/namespaces/") {
		return errors.New("refs under refs/namespaces/ are never writable by a client")
	}

	if u.new == zeroSHA {
		return errors.New("deleting a ref is not permitted")
	}
	if u.old != zeroSHA {
		// Write-once. The refusal lives here rather than in an `update` hook
		// because receive-pack migrates the objects out of quarantine as soon as
		// pre-receive returns 0 — a refusal in `update` would arrive after the
		// objects had already landed.
		return errors.New("this ref already exists and an audit ref is written once")
	}

	tip, err := git.NewSHA(u.new)
	if err != nil {
		return errors.New("malformed object id in the push")
	}

	return checkFreshness(ctx, tip)
}

// checkFreshness enforces the control ADR-0027 added to close the second form of
// the first attack vector of gitnamespaces(7).
//
//	Every object reachable from the new ref value, and not reachable from the
//	mainline, must have been TRANSMITTED by this push.
//
// Without it, a client can name an object it does not hold — either by crafting
// the request outright (measured: probe A3 accepts it in default configuration),
// or, needing no craft at all, by pushing a genuinely new commit whose TREE is
// someone else's. The tip is new so it travels; the tree is not, so it does not;
// and the audit ref then points at another user's code, which its owner reads
// back through the diff of their own run.
func checkFreshness(ctx context.Context, tip git.SHA) error {
	// Two views of the same repository, and the difference between them IS the
	// control:
	//
	//   - `both` reads the quarantine plus the store, because that is what the
	//     enumeration must cover;
	//   - `stored` inherits nothing, so it sees the store alone. Git.command
	//     builds its environment from scratch, which is what makes this view
	//     free rather than something to construct.
	stored := git.New(repositoryDir())
	both := stored.WithObjectDirs(
		os.Getenv("GIT_OBJECT_DIRECTORY"),
		os.Getenv("GIT_ALTERNATE_OBJECT_DIRECTORIES"),
	)

	var excludes []git.Ref
	if raw := os.Getenv(EnvPushMainline); raw != "" {
		mainline, err := git.NewRef(raw)
		if err != nil {
			return errors.New("the configured mainline ref is malformed")
		}
		excludes = append(excludes, mainline)
	}

	// The mainline is the ONLY anchor, and it is written by the server from the
	// forge — never by a client (ADR-0027). Were a client able to set it, it
	// would name a commit holding the tree it covets, see it excluded here, and
	// escape both this control and the secret scan that shares the set.
	arriving, err := both.RevListObjects(ctx, tip, excludes)
	if err != nil {
		return errors.New("the pushed history could not be read")
	}

	ids := make([]git.SHA, 0, len(arriving))
	for _, o := range arriving {
		ids = append(ids, o.SHA)
	}

	present, err := stored.PresentAmong(ctx, ids)
	if err != nil {
		return errors.New("the pushed history could not be verified")
	}
	if len(present) > 0 {
		// The count is safe to report; the identifiers are not. A SHA the server
		// holds and the client does not is exactly what the attacker came for,
		// and echoing it back would confirm the guess.
		return fmt.Errorf("this push claims %d object(s) it did not send", len(present))
	}

	// The scan runs on the SAME set, and that is the point of computing it once:
	// the objects a push adds are exactly the objects nobody has scanned before.
	// Scanning the whole reachable tree instead would re-examine the mainline on
	// every push, and a secret already living there would block every run in the
	// repository, for ever.
	return scanForSecrets(ctx, both, arriving)
}

// scanForSecrets enforces §5.3: mandatory, server-side, before persistence.
//
// It runs here rather than after acceptance because "before persistence" is only
// true inside the hook — once pre-receive returns 0 the objects are migrated out
// of quarantine and the repository has them (ADR-0027, probe A2).
func scanForSecrets(ctx context.Context, repo *git.Git, objects []git.Object) error {
	if len(objects) == 0 {
		return nil
	}

	scanner, err := secrets.New()
	if err != nil {
		// Fail CLOSED. §5.3 says the scan is obligatory, and ADR-0013 removed the
		// temporal safety net behind it: a push accepted because the scanner would
		// not start is a push nobody will ever look at again.
		return errors.New("the secret scanner is unavailable, refusing the push")
	}

	batch, err := repo.OpenBatch(ctx)
	if err != nil {
		return errors.New("the pushed objects could not be read for scanning")
	}
	defer func() { _ = batch.Close() }()

	var findings []secrets.Finding
	for _, obj := range objects {
		kind, body, err := batch.Object(obj.SHA)
		if err != nil {
			return errors.New("a pushed object could not be read for scanning")
		}
		// Trees carry names and modes, no content. Commits are scanned because a
		// token pasted into a commit message is as leaked as one in a file, and
		// leaving them out would be a hole with a well-known shape.
		if kind != "blob" && kind != "commit" {
			continue
		}

		found, err := scanner.Scan(ctx, obj.Path, body)
		if err != nil {
			return errors.New("a pushed object could not be scanned")
		}
		findings = append(findings, found...)

		// Enough to act on, bounded so a push full of secrets cannot turn the
		// refusal message into the payload.
		if len(findings) >= maxReportedFindings {
			break
		}
	}

	if len(findings) == 0 {
		return nil
	}

	var b strings.Builder
	b.WriteString("this push carries secrets and was refused:")
	for _, f := range findings {
		b.WriteString("\n  - ")
		// Rule, path and line. NEVER the value: the message travels back to the
		// client and into its terminal scrollback, and a scanner that echoes what
		// it finds has moved the leak rather than stopped it.
		b.WriteString(f.String())
	}
	b.WriteString("\nremove them from the history, or have an operator add an exception server-side.")
	return errors.New(b.String())
}

// maxReportedFindings bounds the refusal message.
const maxReportedFindings = 20

// repositoryDir is the bare repository the hook was invoked in.
//
// receive-pack runs hooks with the repository as the working directory and
// GIT_DIR set to "." (measured, probe A1). The current directory is therefore the
// answer, and using it rather than GIT_DIR keeps this from depending on a
// relative value whose meaning changes with the process's cwd.
func repositoryDir() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	return dir
}

// readUpdates parses the "<old> <new> <ref>" lines receive-pack writes to the
// hook.
func readUpdates(stdin io.Reader) ([]update, error) {
	var updates []update

	scanner := bufio.NewScanner(stdin)
	// A ref name is bounded well below this; the cap is here so a malformed
	// stream cannot be used to allocate.
	scanner.Buffer(make([]byte, 0, 4096), 64*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			return nil, errors.New("malformed update line")
		}
		updates = append(updates, update{old: fields[0], new: fields[1], ref: fields[2]})
	}
	if err := scanner.Err(); err != nil {
		return nil, errors.New("the update list could not be read")
	}
	if len(updates) == 0 {
		return nil, errors.New("the push contains no ref update")
	}
	return updates, nil
}

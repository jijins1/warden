package git

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
)

// Object is one entry of a rev-list --objects walk.
//
// The path travels with the id because the secret scan needs it: a finding
// reported without the file it sits in tells the user a secret exists and not
// where, which is an alert nobody can act on (§5.7). It is the path as recorded
// in the tree, so it is empty for commits and for root trees.
type Object struct {
	SHA  SHA
	Path string
}

// WithObjectDirs returns a copy that reads objects from an explicit set of
// directories.
//
// This exists for one caller and one purpose: a pre-receive hook, which must see
// the union of the quarantine and the real store. ADR-0027 measured the shape git
// hands it (probe A1):
//
//	GIT_OBJECT_DIRECTORY             = <repo>/objects/tmp_objdir-incoming-XXXX
//	GIT_ALTERNATE_OBJECT_DIRECTORIES = <repo>/objects
//
// The asymmetry with the rest of this package is deliberate and load-bearing.
// Git.command builds its environment from scratch and inherits nothing, so a
// plain New(dir) inside a hook sees the REAL STORE ONLY — the quarantine is
// invisible to it because nothing puts it there. That is precisely the second
// half of the freshness control of ADR-0027: enumerate with both, then test for
// presence with neither.
//
// Passing an empty primary is not an error; it means "leave the default", which
// keeps a caller from having to branch on whether it is running inside a hook.
func (g *Git) WithObjectDirs(primary, alternates string) *Git {
	var kv []string
	if primary != "" {
		kv = append(kv, "GIT_OBJECT_DIRECTORY="+primary)
	}
	if alternates != "" {
		kv = append(kv, "GIT_ALTERNATE_OBJECT_DIRECTORIES="+alternates)
	}
	if len(kv) == 0 {
		return g
	}
	return g.withEnv(kv...)
}

// RevListObjects lists every object reachable from tip and not from excludes.
//
// The result is the exact set a push ADDS to what the excluded refs already
// reach. ADR-0027 uses it for two things at once — the perimeter of the secret
// scan, and the input of the freshness control — and it is one call on purpose:
// two enumerations that could drift apart would be a hole rather than a
// redundancy.
//
// An exclusion ending in "/" is a PREFIX; anything else is an exact ref name.
//
// The distinction is not cosmetic, and getting it wrong fails silently — which is
// how it was wrong. `git rev-list --glob=<pattern>` appends "/*" when the pattern
// has no wildcard, so `--glob=refs/heads/main` becomes `refs/heads/main/*` and
// matches NOTHING. An exclusion passed that way excludes nothing at all, the
// command still succeeds, and the only symptom is a set that is quietly too
// large — which, for the freshness control that consumes it, means honest pushes
// refused for claiming objects they did in fact send.
//
// A prefix matching nothing is not an error, and neither is a missing exact ref:
// the set to exclude is "everything the pusher could already see", and a
// repository that holds none of it yet is a real state.
func (g *Git) RevListObjects(ctx context.Context, tip SHA, excludeGlobs []string) ([]Object, error) {
	if !tip.Valid() {
		return nil, fmt.Errorf("invalid tip SHA: %q", tip)
	}

	// `--not` est émis UNE SEULE FOIS, avant toutes les exclusions.
	//
	// Il « inverse le sens des révisions suivantes JUSQU'AU PROCHAIN --not ». En
	// le répétant devant chaque exclusion, une exclusion sur deux redevenait donc
	// une INCLUSION — et le symptôme est silencieux : la commande réussit, et
	// l'ensemble rendu est simplement faux. Pour le contrôle de fraîcheur qui le
	// consomme, cela signifie des push honnêtes refusés au motif qu'ils
	// revendiquent des objets qu'ils ont pourtant envoyés.
	//
	// La version précédente était correcte par ACCIDENT : le motif du milieu ne
	// correspondait à rien, donc l'inversion parasite n'avait pas d'effet
	// visible. Deux défauts qui s'annulaient.
	args := []string{"rev-list", "--objects", tip.String()}

	exclusions, err := g.refArgs(ctx, excludeGlobs)
	if err != nil {
		return nil, err
	}
	if len(exclusions) > 0 {
		args = append(args, "--not")
		args = append(args, exclusions...)
	}

	// `--` terminates the revision list. Without it a ref named like an option
	// would be read as one, and these refs come from configuration.
	args = append(args, "--")

	return g.revList(ctx, args)
}

// ReachableObjects lists every object reachable from a set of ref patterns.
//
// The inclusive counterpart of the exclusions above, and it exists because those
// exclusions do NOT compute an object set difference. MEASURED on git 2.43:
//
//	rev-list --objects <new> ^<other>
//
// still lists the tree and blobs of <other> when <new> merely REUSES that tree
// without descending from that commit. Git marks the excluded commit and its
// ancestry uninteresting, not every object those commits happen to name — and no
// flag changes it (--objects-edge-aggressive and --boundary were both tried).
//
// That gap is invisible until two things are compared: what a push must send,
// and what the client was entitled to omit. The client omits by REACHABILITY
// FROM WHAT THE SERVER ADVERTISED, which is exactly what this function computes.
func (g *Git) ReachableObjects(ctx context.Context, globs []string) ([]Object, error) {
	inclusions, err := g.refArgs(ctx, globs)
	if err != nil {
		return nil, err
	}
	if len(inclusions) == 0 {
		// No visible ref resolves: a real state for a repository that has only
		// ever been pushed to once, and an empty set is its honest answer.
		return nil, nil
	}

	args := append([]string{"rev-list", "--objects"}, inclusions...)
	return g.revList(ctx, append(args, "--"))
}

// refArgs turns ref patterns into rev-list arguments.
//
// An entry ending in "/" is a PREFIX; anything else is an exact ref name. The
// distinction is not cosmetic, and getting it wrong fails silently — which is how
// it was wrong: `--glob=<pattern>` appends "/*" when the pattern has no wildcard,
// so `--glob=refs/heads/main` becomes `refs/heads/main/*` and matches NOTHING.
func (g *Git) refArgs(ctx context.Context, patterns []string) ([]string, error) {
	out := make([]string, 0, len(patterns))

	for _, glob := range patterns {
		// These patterns become arguments to git and decide what a security
		// control covers. They are validated rather than trusted: `--not` is
		// positional, so a pattern that looked like an option would silently
		// change the meaning of everything after it.
		if !strings.HasPrefix(glob, "refs/") {
			return nil, fmt.Errorf("ref pattern %q must start with refs/", glob)
		}
		if strings.ContainsAny(glob, " \t\n~^:?[\\") || strings.Contains(glob, "..") {
			return nil, fmt.Errorf("ref pattern %q contains a forbidden character", glob)
		}

		if strings.HasSuffix(glob, "/") {
			out = append(out, "--glob="+glob)
			continue
		}

		// Exact ref: named directly, which rev-list refuses if it does not
		// resolve. Hence the existence check — a missing ref is a state, not a
		// failure.
		present, err := g.hasRef(ctx, Ref(glob))
		if err != nil {
			return nil, err
		}
		if present {
			out = append(out, glob)
		}
	}
	return out, nil
}

func (g *Git) revList(ctx context.Context, args []string) ([]Object, error) {
	stdout, stderr, err := g.run(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("git rev-list --objects: %w: %s", err, strings.TrimSpace(stderr))
	}

	var objects []Object
	for _, line := range strings.Split(stdout, "\n") {
		// Each line is "<sha>" or "<sha> <path>". The path may contain spaces, so
		// it is everything after the first separator rather than a second field.
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		id, path, _ := strings.Cut(line, " ")
		sha, err := NewSHA(strings.TrimSpace(id))
		if err != nil {
			return nil, fmt.Errorf("rev-list produced an unreadable object id %q: %w", id, err)
		}
		objects = append(objects, Object{SHA: sha, Path: path})
	}
	return objects, nil
}

// Batch streams object contents from a single long-lived `cat-file --batch`.
//
// One process for a whole push, not one per object. The set this walks is every
// object a push adds, and since ADR-0027's simplified model makes a full local
// history the ordinary case, that is routinely thousands of objects. Spawning
// git per object would make the secret scan of §5.3 expensive enough that
// somebody would eventually turn it off.
type Batch struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	out     *bufio.Reader
	pending int64 // body bytes not yet consumed, plus the record's trailing newline
}

// ErrObjectMissing reports that the batch was asked for an object git does not
// have.
var ErrObjectMissing = errors.New("object missing")

// OpenBatch starts the reader. Close must be called.
func (g *Git) OpenBatch(ctx context.Context) (*Batch, error) {
	cmd := g.command(ctx, "cat-file", "--batch")

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting cat-file --batch: %w", err)
	}
	return &Batch{cmd: cmd, stdin: stdin, out: bufio.NewReaderSize(stdout, 64<<10)}, nil
}

// Object asks for one object and returns its type and a reader over its body.
//
// The body need not be read to the end: the next call skips whatever is left.
// Leaving that to the caller would make a `continue` in a loop silently
// desynchronise the stream, and a desynchronised scan reads one object's bytes
// as another's.
func (b *Batch) Object(sha SHA) (kind string, body io.Reader, err error) {
	if !sha.Valid() {
		return "", nil, fmt.Errorf("invalid object SHA: %q", sha)
	}
	if err := b.skip(); err != nil {
		return "", nil, err
	}
	if _, err := io.WriteString(b.stdin, sha.String()+"\n"); err != nil {
		return "", nil, fmt.Errorf("requesting object %s: %w", sha, err)
	}

	header, err := b.out.ReadString('\n')
	if err != nil {
		return "", nil, fmt.Errorf("reading the header of object %s: %w", sha, err)
	}
	fields := strings.Fields(strings.TrimSpace(header))
	if len(fields) == 2 && fields[1] == "missing" {
		return "", nil, fmt.Errorf("%w: %s", ErrObjectMissing, sha)
	}
	if len(fields) != 3 {
		return "", nil, fmt.Errorf("unreadable cat-file header for %s", sha)
	}
	size, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil || size < 0 {
		return "", nil, fmt.Errorf("unreadable object size for %s", sha)
	}

	b.pending = size + 1 // git writes a newline after each body
	return fields[1], &batchBody{batch: b, left: size}, nil
}

// skip drains whatever remains of the previous record.
func (b *Batch) skip() error {
	if b.pending <= 0 {
		return nil
	}
	if _, err := io.CopyN(io.Discard, b.out, b.pending); err != nil {
		return fmt.Errorf("draining the previous object: %w", err)
	}
	b.pending = 0
	return nil
}

// Close ends the stream and reaps the process.
func (b *Batch) Close() error {
	_ = b.stdin.Close()
	// The remaining stdout is discarded rather than read: git is being asked to
	// stop, and blocking on its output here would deadlock if it has more to say
	// than the pipe holds.
	_ = b.cmd.Wait()
	return nil
}

type batchBody struct {
	batch *Batch
	left  int64
}

func (r *batchBody) Read(p []byte) (int, error) {
	if r.left <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > r.left {
		p = p[:r.left]
	}
	n, err := r.batch.out.Read(p)
	r.left -= int64(n)
	r.batch.pending -= int64(n)
	return n, err
}

// PresentAmong returns those of the given objects that this repository already
// holds.
//
// One git process for the whole set, not one per object. That is not a
// micro-optimisation: ADR-0027 made the caller of this the freshness control,
// whose input is every object a push adds — and since the simplified git model
// makes a full local history the NORMAL case rather than an edge one, that set is
// routinely thousands of objects. Per-object spawning would turn a security
// control into a reason to disable it.
//
// The empty input returns nothing without running git, because `cat-file --batch`
// on an empty stdin is a process spawned to learn what the caller already knows.
func (g *Git) PresentAmong(ctx context.Context, objects []SHA) ([]SHA, error) {
	if len(objects) == 0 {
		return nil, nil
	}

	var input strings.Builder
	for _, sha := range objects {
		if !sha.Valid() {
			return nil, fmt.Errorf("invalid object SHA: %q", sha)
		}
		input.WriteString(sha.String())
		input.WriteString("\n")
	}

	// --batch-check reads ids on stdin and answers one line each: either
	// "<sha> <type> <size>" or "<sha> missing". It exits 0 either way, so a
	// non-zero status here is a real failure and must not be read as "all
	// missing" — that reading would make every object look new and silently
	// disable the control.
	stdout, stderr, err := g.runStdin(ctx, []byte(input.String()), "cat-file", "--batch-check")
	if err != nil {
		return nil, fmt.Errorf("git cat-file --batch-check: %w: %s", err, strings.TrimSpace(stderr))
	}

	var present []SHA
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasSuffix(line, " missing") {
			continue
		}
		field, _, _ := strings.Cut(line, " ")
		sha, err := NewSHA(field)
		if err != nil {
			return nil, fmt.Errorf("cat-file produced an unreadable object id %q: %w", field, err)
		}
		present = append(present, sha)
	}
	return present, nil
}

// hasRef reports whether a ref resolves in this repository.
func (g *Git) hasRef(ctx context.Context, ref Ref) (bool, error) {
	_, _, err := g.run(ctx, "rev-parse", "--verify", "--quiet", ref.String()+"^{commit}")
	if err == nil {
		return true, nil
	}
	// --quiet makes a missing ref exit 1 with no output, which is the answer
	// rather than a failure. Anything else is a real error and must not be read
	// as "absent" — that would silently widen the object set, which is the same
	// class of silent widening the glob bug above produced.
	if isExitCode(err, 1) {
		return false, nil
	}
	return false, fmt.Errorf("resolving ref %s: %w", ref, err)
}

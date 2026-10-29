package git

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrPathMissing reports that a commit's tree does not contain the given path.
//
// Distinct from a git failure on purpose. A project that has not yet committed a
// configuration file is an ordinary state, not a breakdown, and the caller
// decides what it means — this package does not.
var ErrPathMissing = errors.New("path missing from commit")

// ErrBlobTooLarge reports that a blob exists but exceeds the caller's limit.
//
// A sentinel rather than an opaque message because two callers read it
// differently: the configuration reader treats it as a malformed request, the
// file browser of ADR-0055 tells the user the file is too large to show.
var ErrBlobTooLarge = errors.New("blob exceeds the size limit")

// BlobAt reads one blob addressed by commit and path, bounded by limit.
//
// The size is settled BEFORE the body is read. `cat-file --batch-check` answers
// with type and size without emitting a byte of content, so an oversized blob
// costs one small process instead of crossing the wire to be thrown away. That
// matters here because the caller is on a run's hot path and the repository is
// untrusted input.
//
// The path is not a pathspec and no globbing happens: `<commit>:<path>` is git's
// own revision syntax, resolved against the commit's tree.
func (g *Git) BlobAt(ctx context.Context, commit SHA, path string, limit int64) ([]byte, error) {
	if !commit.Valid() {
		return nil, fmt.Errorf("invalid commit SHA: %q", commit)
	}
	if err := cheminDeBlob(path); err != nil {
		return nil, err
	}

	rev := commit.String() + ":" + path

	// --batch-check rather than `cat-file -t` followed by `-s`: one process
	// instead of two, and it reports a missing path on STDOUT as a normal record
	// rather than through stderr, which spares us parsing git's prose.
	entete, stderr, err := g.runStdin(ctx, []byte(rev+"\n"), "cat-file", "--batch-check")
	if err != nil {
		return nil, fmt.Errorf("git cat-file --batch-check: %w: %s", err, strings.TrimSpace(stderr))
	}

	champs := strings.Fields(strings.TrimSpace(entete))
	if len(champs) == 0 {
		return nil, fmt.Errorf("unreadable cat-file header for %s", rev)
	}
	// git echoes the input then " missing". The input holds the path, which may
	// itself contain spaces, so the LAST field is the one to read.
	if champs[len(champs)-1] == "missing" {
		return nil, fmt.Errorf("%w: %s", ErrPathMissing, path)
	}
	if len(champs) != 3 {
		return nil, fmt.Errorf("unreadable cat-file header for %s: %q", rev, entete)
	}
	if champs[1] != "blob" {
		// A directory resolves fine and is not missing; handing its tree object
		// back would have the caller parse binary as text.
		return nil, fmt.Errorf("%s is a %s, not a blob", path, champs[1])
	}
	taille, err := strconv.ParseInt(champs[2], 10, 64)
	if err != nil || taille < 0 {
		return nil, fmt.Errorf("unreadable object size for %s", rev)
	}
	if taille > limit {
		return nil, fmt.Errorf("%w: %s: %d octets, %d au plus", ErrBlobTooLarge, path, taille, limit)
	}

	corps, stderr, err := g.runLimited(ctx, limit, "cat-file", "blob", rev)
	if err != nil {
		return nil, fmt.Errorf("git cat-file blob: %w: %s", err, strings.TrimSpace(stderr))
	}
	return []byte(corps), nil
}

// cheminDeBlob rejects paths before they reach git.
//
// The newline is the one that matters and the reason this is not merely
// hygiene: the path travels on the stdin of `cat-file --batch-check`, whose
// protocol is one request per line. A `\n` would smuggle a second request into
// the same call, and the caller would read the answer to a question it never
// asked.
func cheminDeBlob(p string) error {
	if p == "" {
		return errors.New("blob path is empty")
	}
	if strings.ContainsAny(p, "\n\x00") {
		return fmt.Errorf("blob path contains a newline or NUL: %q", p)
	}
	if strings.HasPrefix(p, "/") {
		return fmt.Errorf("blob path must be relative to the tree: %q", p)
	}
	if strings.HasPrefix(p, "-") {
		// git would read it as an option. It never reaches argv here — it is
		// concatenated after `<sha>:` — but the rule holds for whoever moves
		// this call one day.
		return fmt.Errorf("blob path must not start with a dash: %q", p)
	}
	for _, segment := range strings.Split(p, "/") {
		if segment == ".." {
			return fmt.Errorf("blob path escapes the tree: %q", p)
		}
	}
	return nil
}

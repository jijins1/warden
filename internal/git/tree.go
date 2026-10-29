package git

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// TreeFile is one file of a commit's tree, as the browser of ADR-0055 lists it.
type TreeFile struct {
	Path string
	Size int64
}

// maxTreeBytes bounds the raw `ls-tree` output, independently of the entry
// count. Twenty thousand entries with ordinary paths stay well under it; a tree
// of pathological path lengths is cut here rather than buffered whole.
const maxTreeBytes = 16 << 20

// TreeAt lists every file of a commit's tree, recursively, up to maxEntries.
//
// Files only: directories are implied by the paths, and a submodule is a
// commit of ANOTHER repository whose content is not here to read. Symlinks are
// blobs and are kept — their content is the target, which is what was run.
//
// The second result is true when the list was cut, by entry count or by the
// size of git's output. A cut list is not reported as complete: an auditor
// reading it would conclude the files beyond the bound do not exist.
//
// `-z` because a path may hold a tab or a newline, which the default output
// would quote and this parser would then have to unquote.
func (g *Git) TreeAt(ctx context.Context, commit SHA, maxEntries int) ([]TreeFile, bool, error) {
	if !commit.Valid() {
		return nil, false, fmt.Errorf("invalid commit SHA: %q", commit)
	}

	sortie, stderr, err := g.runLimited(ctx, maxTreeBytes, "ls-tree", "-r", "-z", "--long", commit.String())
	if err != nil {
		return nil, false, fmt.Errorf("git ls-tree: %w: %s", err, strings.TrimSpace(stderr))
	}
	coupe := int64(len(sortie)) > maxTreeBytes
	if coupe {
		// The last record is incomplete by construction.
		sortie = sortie[:maxTreeBytes]
		if i := strings.LastIndexByte(sortie, 0); i >= 0 {
			sortie = sortie[:i]
		} else {
			sortie = ""
		}
	}

	var fichiers []TreeFile
	for _, enregistrement := range strings.Split(sortie, "\x00") {
		if enregistrement == "" {
			continue
		}
		// <mode> SP <type> SP <object> SP+ <size> TAB <path>
		entete, chemin, ok := strings.Cut(enregistrement, "\t")
		if !ok {
			return nil, false, fmt.Errorf("unreadable ls-tree record: %q", enregistrement)
		}
		champs := strings.Fields(entete)
		if len(champs) != 4 {
			return nil, false, fmt.Errorf("unreadable ls-tree record: %q", enregistrement)
		}
		if champs[1] != "blob" {
			continue
		}
		if len(fichiers) == maxEntries {
			return fichiers, true, nil
		}
		taille, err := strconv.ParseInt(champs[3], 10, 64)
		if err != nil {
			return nil, false, fmt.Errorf("unreadable size in ls-tree record: %q", enregistrement)
		}
		fichiers = append(fichiers, TreeFile{Path: chemin, Size: taille})
	}
	return fichiers, coupe, nil
}

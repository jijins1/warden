package gitclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/jijins1/warden/internal/server/internal/core"
	"github.com/jijins1/warden/internal/server/internal/gitwire"
)

// Tree liste les fichiers de l'arbre du commit audité du run (ADR-0055).
//
// Il satisfait core.RepoBrowser avec File. Le commit est celui du run, jamais un
// SHA venu de l'appelant : c'est le code qui a tourné qu'on montre.
func (c *Client) Tree(ctx context.Context, run core.Run) (core.RepoTree, error) {
	if run.Repo == "" {
		// Run antérieur à la migration 00004 : inutile d'aller le demander.
		return core.RepoTree{}, fmt.Errorf("dépôt inconnu: %w", core.ErrArtifactGone)
	}

	resp, err := c.appeler(ctx, gitwire.TreePath, gitwire.TreeRequest{Repo: run.Repo, SHA: run.AuditSHA})
	if err != nil {
		return core.RepoTree{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return core.RepoTree{}, c.erreur(resp)
	}

	// La borne de lecture couvre MaxTreeEntries chemins de longueur ordinaire.
	// C'est la mémoire de CE processus qu'elle protège, comme pour le diff.
	var rep gitwire.TreeResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, gitwire.MaxDiffBytes)).Decode(&rep); err != nil {
		return core.RepoTree{}, fmt.Errorf("lecture de l'arbre: %w", err)
	}

	arbre := core.RepoTree{Files: make([]core.RepoFile, 0, len(rep.Files)), Truncated: rep.Truncated}
	for _, f := range rep.Files {
		arbre.Files = append(arbre.Files, core.RepoFile{Path: f.Path, Size: f.Size})
	}
	return arbre, nil
}

// File lit un fichier de l'arbre du commit audité du run (ADR-0055).
//
// Un chemin absent se dit core.ErrPathMissing et non core.ErrNoProjectConfig :
// le code d'erreur du rôle `git` est le même que pour la configuration, mais
// l'appelant ne l'est pas, et c'est lui qui donne son sens à l'absence.
func (c *Client) File(ctx context.Context, run core.Run, path string) ([]byte, error) {
	if run.Repo == "" {
		return nil, fmt.Errorf("dépôt inconnu: %w", core.ErrArtifactGone)
	}

	resp, err := c.appeler(ctx, gitwire.FilePath, gitwire.FileRequest{Repo: run.Repo, SHA: run.AuditSHA, Path: path})
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		e := lireErreur(resp)
		switch e.Code {
		case gitwire.CodeUnknownPath:
			return nil, fmt.Errorf("%s: %w", e.Message, core.ErrPathMissing)
		case gitwire.CodeTooLarge:
			return nil, fmt.Errorf("%s: %w", e.Message, core.ErrFileTooLarge)
		default:
			return nil, c.traduire(resp.StatusCode, e)
		}
	}

	contenu, err := io.ReadAll(io.LimitReader(resp.Body, gitwire.MaxFileBytes))
	if err != nil {
		return nil, fmt.Errorf("lecture du fichier: %w", err)
	}
	return contenu, nil
}

// appeler poste un ordre à l'API interne du rôle `git`.
//
// Le contexte borné est attaché à la requête et libéré quand le corps est
// fermé : la lecture de la réponse reste couverte par le délai du client.
func (c *Client) appeler(ctx context.Context, chemin string, ordre any) (*http.Response, error) {
	corps, err := json.Marshal(ordre)
	if err != nil {
		return nil, fmt.Errorf("encodage de la requête: %w", err)
	}

	ctx, annuler := context.WithTimeout(ctx, c.timeout)
	cible := c.base.JoinPath(chemin)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cible.String(), bytes.NewReader(corps))
	if err != nil {
		annuler()
		return nil, fmt.Errorf("construction de la requête: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpc.Do(req)
	if err != nil {
		annuler()
		// %w garde context.DeadlineExceeded reconnaissable : l'API en fait un 504.
		return nil, fmt.Errorf("appel du rôle git: %w", err)
	}
	resp.Body = corpsAnnulable{ReadCloser: resp.Body, annuler: annuler}
	return resp, nil
}

// corpsAnnulable libère le contexte de l'appel à la fermeture du corps.
type corpsAnnulable struct {
	io.ReadCloser
	annuler context.CancelFunc
}

func (b corpsAnnulable) Close() error {
	err := b.ReadCloser.Close()
	b.annuler()
	return err
}

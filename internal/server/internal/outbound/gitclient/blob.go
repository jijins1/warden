package gitclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/jijins1/warden/internal/config"
	"github.com/jijins1/warden/internal/server/internal/core"
	"github.com/jijins1/warden/internal/server/internal/gitwire"
)

// ReadProjectConfig lit `.warden.yml` dans l'arbre du commit, ou à défaut
// l'ancien `.ansibleproxy.yml` (ADR-0056).
//
// Il satisfait core.ProjectConfigReader.
//
// Le NOM du fichier est composé ici, du côté qui connaît la règle de run, et
// jamais par le rôle `git` : celui-ci reçoit un chemin et ignore ce qu'il porte
// (voir gitwire.BlobRequest). Les noms viennent de `config.Files` plutôt que de
// littéraux, pour que la CLI qui lit le worktree et le serveur qui lit l'arbre
// ne puissent pas diverger — ni sur les noms, ni sur leur ordre.
func (c *Client) ReadProjectConfig(ctx context.Context, repo, sha string) ([]byte, error) {
	if repo == "" || sha == "" {
		return nil, fmt.Errorf("lecture de configuration : dépôt et commit sont requis")
	}

	var err error
	for _, nom := range config.Files {
		var contenu []byte
		contenu, err = c.lireBlob(ctx, repo, sha, nom)
		if !errors.Is(err, core.ErrNoProjectConfig) {
			return contenu, err
		}
	}
	return nil, err
}

func (c *Client) lireBlob(ctx context.Context, repo, sha, chemin string) ([]byte, error) {
	ctx, annuler := context.WithTimeout(ctx, c.timeout)
	defer annuler()

	corps, err := json.Marshal(gitwire.BlobRequest{Repo: repo, SHA: sha, Path: chemin})
	if err != nil {
		return nil, fmt.Errorf("encodage de la requête: %w", err)
	}

	cible := c.base.JoinPath(gitwire.BlobPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cible.String(), bytes.NewReader(corps))
	if err != nil {
		return nil, fmt.Errorf("construction de la requête: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("appel du rôle git: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, c.erreur(resp)
	}

	// La borne est celle du contrat, appliquée même si le service la respecte :
	// c'est la mémoire de CE processus qu'elle protège.
	contenu, err := io.ReadAll(io.LimitReader(resp.Body, gitwire.MaxBlobBytes))
	if err != nil {
		return nil, fmt.Errorf("lecture de la configuration: %w", err)
	}
	return contenu, nil
}

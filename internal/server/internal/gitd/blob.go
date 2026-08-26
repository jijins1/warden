package gitd

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/jijins1/warden/internal/git"
	"github.com/jijins1/warden/internal/server/internal/gitwire"
)

// handleBlob rend le contenu d'un fichier dans l'arbre d'un commit.
//
// Comme handleDiff, il ne connaît pas la notion de run : il reçoit un dépôt, un
// commit et un chemin. Que ce chemin soit celui d'un fichier de configuration,
// et ce que cette configuration décide, appartient au rôle `api` (ADR-0009).
func (s *Server) handleBlob(w http.ResponseWriter, r *http.Request) {
	var req gitwire.BlobRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, tailleMaxRequete))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		repondreErreur(w, &gitwire.Error{
			Code:    gitwire.CodeBadRequest,
			Message: "corps de requête illisible",
		})
		return
	}

	sha, err := git.NewSHA(req.SHA)
	if err != nil {
		repondreErreur(w, &gitwire.Error{Code: gitwire.CodeBadRequest, Message: "SHA invalide"})
		return
	}

	dir, errDepot := s.depots.dir(req.Repo)
	if errDepot != nil {
		repondreErreur(w, errDepot)
		return
	}

	// L'existence du COMMIT est vérifiée à part, avant la lecture du chemin.
	// Sans ce passage, un commit purgé (ADR-0013) et un chemin absent
	// produiraient le même refus, et l'appelant tolère l'un mais pas l'autre.
	present, err := git.New(dir).HasObject(r.Context(), sha)
	if err != nil {
		slog.Error("gitd: cat-file", "repo", req.Repo, "error", err)
		repondreErreur(w, &gitwire.Error{Code: gitwire.CodeInternal, Message: "moteur git indisponible"})
		return
	}
	if !present {
		repondreErreur(w, &gitwire.Error{
			Code:    gitwire.CodeUnknownObject,
			Message: "commit absent du dépôt",
		})
		return
	}

	contenu, err := git.New(dir).BlobAt(r.Context(), sha, req.Path, gitwire.MaxBlobBytes)
	switch {
	case errors.Is(err, git.ErrPathMissing):
		repondreErreur(w, &gitwire.Error{
			Code:    gitwire.CodeUnknownPath,
			Message: "ce chemin n'existe pas dans l'arbre de ce commit",
		})
		return
	case err != nil:
		// La stderr de git cite volontiers des chemins du dépôt audité : elle va
		// dans le journal du service, jamais dans la réponse.
		slog.Error("gitd: lecture de blob", "repo", req.Repo, "error", err)
		repondreErreur(w, &gitwire.Error{
			Code:    gitwire.CodeBadRequest,
			Message: "ce chemin n'est pas un fichier lisible, ou il dépasse la taille acceptée",
		})
		return
	}

	w.Header().Set("Content-Type", gitwire.ContentTypeBlob)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// #nosec G705 -- servi en octet-stream avec nosniff, jamais rendu comme du
	// HTML. Le contenu vient du dépôt audité et n'est pas de confiance.
	_, _ = w.Write(contenu)
}

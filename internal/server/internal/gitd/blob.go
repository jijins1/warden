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
	if !decoder(w, r, &req) {
		return
	}
	// Un dépassement reste ici une requête refusée, comme avant l'ADR-0055 :
	// le lecteur de configuration n'a pas à distinguer « trop gros » d'un
	// chemin illisible, le schéma de configuration le borne déjà.
	s.servirBlob(w, r, req.Repo, req.SHA, req.Path, gitwire.MaxBlobBytes, false)
}

// handleFile rend un fichier de l'arbre pour le parcours de l'ADR-0055.
//
// Même lecture que handleBlob, sous une borne plus large, et un dépassement
// dit comme tel : l'interface l'annonce à l'auditeur au lieu d'une erreur.
func (s *Server) handleFile(w http.ResponseWriter, r *http.Request) {
	var req gitwire.FileRequest
	if !decoder(w, r, &req) {
		return
	}
	s.servirBlob(w, r, req.Repo, req.SHA, req.Path, gitwire.MaxFileBytes, true)
}

// handleTree liste les fichiers de l'arbre d'un commit (ADR-0055).
func (s *Server) handleTree(w http.ResponseWriter, r *http.Request) {
	var req gitwire.TreeRequest
	if !decoder(w, r, &req) {
		return
	}
	g, sha, ok := s.commitPresent(w, r, req.Repo, req.SHA)
	if !ok {
		return
	}

	fichiers, coupe, err := g.TreeAt(r.Context(), sha, gitwire.MaxTreeEntries)
	if err != nil {
		// La stderr de git cite volontiers des chemins du dépôt audité : elle va
		// dans le journal du service, jamais dans la réponse.
		slog.Error("gitd: liste de l'arbre", "repo", req.Repo, "error", err)
		repondreErreur(w, &gitwire.Error{Code: gitwire.CodeInternal, Message: "liste de l'arbre impossible"})
		return
	}

	rep := gitwire.TreeResponse{Files: make([]gitwire.TreeFile, 0, len(fichiers)), Truncated: coupe}
	for _, f := range fichiers {
		rep.Files = append(rep.Files, gitwire.TreeFile{Path: f.Path, Size: f.Size})
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_ = json.NewEncoder(w).Encode(rep)
}

// decoder lit le corps JSON d'une requête interne, et répond lui-même au refus.
func decoder(w http.ResponseWriter, r *http.Request, cible any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, tailleMaxRequete))
	dec.DisallowUnknownFields()
	if err := dec.Decode(cible); err != nil {
		repondreErreur(w, &gitwire.Error{
			Code:    gitwire.CodeBadRequest,
			Message: "corps de requête illisible",
		})
		return false
	}
	return true
}

// commitPresent résout le dépôt et vérifie que le commit y est, avant toute
// lecture de son arbre.
//
// L'existence du COMMIT est vérifiée à part. Sans ce passage, un commit purgé
// (ADR-0013) et un chemin absent produiraient le même refus, et l'appelant
// tolère l'un mais pas l'autre.
func (s *Server) commitPresent(w http.ResponseWriter, r *http.Request, repo, brut string) (*git.Git, git.SHA, bool) {
	sha, err := git.NewSHA(brut)
	if err != nil {
		repondreErreur(w, &gitwire.Error{Code: gitwire.CodeBadRequest, Message: "SHA invalide"})
		return nil, "", false
	}

	dir, errDepot := s.depots.dir(repo)
	if errDepot != nil {
		repondreErreur(w, errDepot)
		return nil, "", false
	}

	g := git.New(dir)
	present, err := g.HasObject(r.Context(), sha)
	if err != nil {
		slog.Error("gitd: cat-file", "repo", repo, "error", err)
		repondreErreur(w, &gitwire.Error{Code: gitwire.CodeInternal, Message: "moteur git indisponible"})
		return nil, "", false
	}
	if !present {
		repondreErreur(w, &gitwire.Error{
			Code:    gitwire.CodeUnknownObject,
			Message: "commit absent du dépôt",
		})
		return nil, "", false
	}
	return g, sha, true
}

// servirBlob lit un blob sous une borne et l'écrit dans la réponse.
//
// `tropGrosDistinct` choisit comment se dit un dépassement : CodeTooLarge pour
// le parcours de fichiers, la requête refusée d'origine pour le lecteur de
// configuration.
func (s *Server) servirBlob(w http.ResponseWriter, r *http.Request, repo, brut, chemin string, limite int64, tropGrosDistinct bool) {
	g, sha, ok := s.commitPresent(w, r, repo, brut)
	if !ok {
		return
	}

	contenu, err := g.BlobAt(r.Context(), sha, chemin, limite)
	switch {
	case errors.Is(err, git.ErrPathMissing):
		repondreErreur(w, &gitwire.Error{
			Code:    gitwire.CodeUnknownPath,
			Message: "ce chemin n'existe pas dans l'arbre de ce commit",
		})
		return
	case tropGrosDistinct && errors.Is(err, git.ErrBlobTooLarge):
		repondreErreur(w, &gitwire.Error{
			Code:    gitwire.CodeTooLarge,
			Message: "ce fichier dépasse la taille servie",
		})
		return
	case err != nil:
		// La stderr de git cite volontiers des chemins du dépôt audité : elle va
		// dans le journal du service, jamais dans la réponse.
		slog.Error("gitd: lecture de blob", "repo", repo, "error", err)
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

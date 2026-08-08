package gitd

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/jijins1/warden/internal/git"
	"github.com/jijins1/warden/internal/server/internal/gitwire"
)

// tailleMaxRequete borne le corps accepté. Une DiffRequest fait quelques
// centaines d'octets ; tout ce qui dépasse est une erreur ou une sonde.
const tailleMaxRequete = 4 << 10

// handleDiff sert le seul endpoint de l'API interne.
//
// Il ne connaît pas la notion de run : il reçoit deux objets et le dépôt où les
// trouver. Choisir QUELS objets — et notamment quel est le run précédent — est
// une règle de run, qui appartient au rôle `api` et à sa politique d'accès.
func (s *Server) handleDiff(w http.ResponseWriter, r *http.Request) {
	var req gitwire.DiffRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, tailleMaxRequete))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		repondreErreur(w, &gitwire.Error{
			Code:    gitwire.CodeBadRequest,
			Message: "corps de requête illisible",
		})
		return
	}

	from, err := git.NewSHA(req.From)
	if err != nil {
		repondreErreur(w, &gitwire.Error{Code: gitwire.CodeBadRequest, Message: "SHA `from` invalide"})
		return
	}
	to, err := git.NewSHA(req.To)
	if err != nil {
		repondreErreur(w, &gitwire.Error{Code: gitwire.CodeBadRequest, Message: "SHA `to` invalide"})
		return
	}

	dir, errDepot := s.depots.dir(req.Repo)
	if errDepot != nil {
		repondreErreur(w, errDepot)
		return
	}

	g := git.New(dir)

	// Existence vérifiée AVANT le diff. Sans ce passage, un objet purgé et un
	// moteur en panne produisent la même erreur, et l'ADR-0013 veut que l'audit
	// distingue « le code n'est plus là » de « le service est cassé ».
	for _, sha := range []git.SHA{from, to} {
		present, err := g.HasObject(r.Context(), sha)
		if err != nil {
			slog.Error("gitd: cat-file", "repo", req.Repo, "error", err)
			repondreErreur(w, &gitwire.Error{Code: gitwire.CodeInternal, Message: "moteur git indisponible"})
			return
		}
		if !present {
			repondreErreur(w, &gitwire.Error{
				Code:    gitwire.CodeUnknownObject,
				Message: "objet absent du dépôt",
			})
			return
		}
	}

	diff, tronque, err := g.DiffLimit(r.Context(), from, to, gitwire.MaxDiffBytes)
	if err != nil {
		// La stderr de git cite volontiers des chemins du dépôt audité : elle va
		// dans le journal du service, jamais dans la réponse.
		slog.Error("gitd: diff", "repo", req.Repo, "error", err)
		repondreErreur(w, &gitwire.Error{Code: gitwire.CodeInternal, Message: "calcul du diff impossible"})
		return
	}

	w.Header().Set("Content-Type", gitwire.ContentTypeDiff)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if tronque {
		w.Header().Set(gitwire.HeaderTruncated, "true")
	}
	// #nosec G705 -- servi en text/plain avec nosniff, jamais rendu comme du
	// HTML. Le contenu vient du dépôt audité et n'est pas de confiance.
	_, _ = w.Write([]byte(diff))
}

func repondreErreur(w http.ResponseWriter, e *gitwire.Error) {
	w.Header().Set("Content-Type", gitwire.ContentTypeError)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(e.Code.HTTPStatus())
	_ = json.NewEncoder(w).Encode(e)
}

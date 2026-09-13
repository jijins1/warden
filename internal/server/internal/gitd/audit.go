package gitd

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jijins1/warden/internal/git"
	"github.com/jijins1/warden/internal/server/internal/gitwire"
)

// auditRefPrefix borne ce que cet endpoint accepte d'écrire.
//
// `refs/audit/` et rien d'autre. Le hook `pre-receive` applique la même borne au
// chemin d'écriture des clients (prereceive.go:77), et elle doit valoir aussi
// ici : sans elle, ce chemin serveur pourrait déplacer `refs/warden/mainline` —
// l'ANCRE du contrôle de fraîcheur — ou fabriquer une `refs/namespaces/`, le
// premier vecteur de gitnamespaces(7).
const auditRefPrefix = "refs/audit/"

// handleAuditRef pointe une ref d'audit sur un commit existant, sans push.
//
// Servi sur le listener INTERNE, comme le reste : un pod runner qui pourrait
// appeler cet endpoint fabriquerait la preuve qu'un run a été audité sans
// qu'aucun artefact ne l'ait jamais été.
//
// IDEMPOTENT sur le MÊME commit : un rejeu rend `created: false` sans erreur.
// Sur un commit DIFFÉRENT, la ref est refusée — c'est la seule chose qu'un
// journal d'audit ne doit jamais permettre (ADR-0027).
func (s *Server) handleAuditRef(w http.ResponseWriter, r *http.Request) {
	var req gitwire.AuditRefRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, tailleMaxRequete))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		repondreErreur(w, &gitwire.Error{Code: gitwire.CodeBadRequest, Message: "corps de requête illisible"})
		return
	}

	ref, err := git.NewRef(req.Ref)
	if err != nil {
		repondreErreur(w, &gitwire.Error{Code: gitwire.CodeBadRequest, Message: "ref invalide"})
		return
	}
	if !strings.HasPrefix(ref.String(), auditRefPrefix) {
		repondreErreur(w, &gitwire.Error{
			Code:    gitwire.CodeBadRequest,
			Message: "seules les refs d'audit sont écrites par ce chemin",
		})
		return
	}

	commit, err := git.NewSHA(req.CommitSHA)
	if err != nil {
		repondreErreur(w, &gitwire.Error{Code: gitwire.CodeBadRequest, Message: "SHA invalide"})
		return
	}

	dir, errDepot := s.depots.dir(req.Repo)
	if errDepot != nil {
		repondreErreur(w, errDepot)
		return
	}
	depot := git.New(dir)

	// L'objet doit exister AVANT que la ref le désigne, pour la même raison que
	// handleNamespace : une ref vers un objet absent ferait échouer une lecture
	// bien plus tard, très loin de la cause.
	present, err := depot.HasObject(r.Context(), commit)
	if err != nil {
		repondreErreur(w, &gitwire.Error{Code: gitwire.CodeInternal, Message: "dépôt illisible"})
		return
	}
	if !present {
		repondreErreur(w, &gitwire.Error{
			Code:    gitwire.CodeUnknownObject,
			Message: "ce commit n'est pas dans le dépôt",
		})
		return
	}

	if err := depot.CreateRef(r.Context(), ref, commit); err != nil {
		if errors.Is(err, git.ErrRefExists) {
			existant, errResolve := depot.Resolve(r.Context(), ref.String())
			if errResolve == nil && existant == commit {
				// Rejeu : cette ref désigne déjà EXACTEMENT ce commit. Un
				// succès, pas une erreur — l'appelant ne doit pas avoir à
				// distinguer « déjà fait » de « en train de faire ».
				repondreAuditRef(w, false)
				return
			}
			repondreErreur(w, &gitwire.Error{
				Code:    gitwire.CodeRefExists,
				Message: "cette ref d'audit désigne déjà un autre commit",
			})
			return
		}
		slog.Error("écriture de la ref d'audit en échec",
			slog.String("repo", req.Repo), slog.String("ref", req.Ref), slog.String("error", err.Error()))
		repondreErreur(w, &gitwire.Error{Code: gitwire.CodeInternal, Message: "la ref d'audit n'a pas pu être écrite"})
		return
	}

	repondreAuditRef(w, true)
}

func repondreAuditRef(w http.ResponseWriter, cree bool) {
	w.Header().Set("Content-Type", gitwire.ContentTypeError)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if err := json.NewEncoder(w).Encode(gitwire.AuditRefResponse{Created: cree}); err != nil {
		slog.Error("réponse de ref d'audit non écrite", slog.String("error", err.Error()))
	}
}

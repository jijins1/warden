package gitd

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/jijins1/warden/internal/git"
	"github.com/jijins1/warden/internal/server/internal/gitwire"
)

// gitMainlinePointer est MainlinePointer typé pour le socle.
var gitMainlinePointer = git.Ref(MainlinePointer)

// handleMirror rafraîchit le miroir de la branche protégée d'un dépôt.
//
// Servi sur le listener INTERNE, jamais sur le smart HTTP. La distinction n'est
// pas de rangement : cet endpoint fait SORTIR une connexion du rôle `git` vers
// une adresse qui figure dans la requête. Exposé à un pod runner, il donnerait à
// du code arbitraire un moyen de faire émettre des requêtes par le composant qui
// détient le volume des dépôts.
//
// C'est aussi le premier endpoint à ouvrir un egress. L'ADR-0027 le dit : « pendant
// son indisponibilité, aucun run ne démarre dans l'organisation » couvre
// désormais la disponibilité de la forge.
func (s *Server) handleMirror(w http.ResponseWriter, r *http.Request) {
	var req gitwire.MirrorRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, tailleMaxRequete))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		repondreErreur(w, &gitwire.Error{
			Code:    gitwire.CodeBadRequest,
			Message: "corps de requête illisible",
		})
		return
	}

	ref, err := git.NewRef(req.Ref)
	if err != nil {
		repondreErreur(w, &gitwire.Error{Code: gitwire.CodeBadRequest, Message: "ref de branche invalide"})
		return
	}

	// Revalidée ici bien que l'appelant soit le rôle `api`. `ParseRemote` refuse
	// tout ce qui n'est pas http/https, donc `ext::` — qui prend une commande
	// shell et l'exécute — et `file://`, qui atteindrait les dépôts voisins sur
	// le volume. L'URL vient de la configuration serveur, mais elle traverse une
	// requête, et le contrôle coûte une ligne.
	if _, err := git.ParseRemote(req.Remote); err != nil {
		repondreErreur(w, &gitwire.Error{Code: gitwire.CodeBadRequest, Message: "URL de forge refusée"})
		return
	}

	dir, errDepot := s.depots.dir(req.Repo)
	if errDepot != nil {
		repondreErreur(w, errDepot)
		return
	}

	depot := git.New(dir)
	sha, err := depot.Fetch(r.Context(), req.Remote, s.cfg.ForgeToken, ref)
	if err != nil {
		// La stderr de git n'entre PAS dans la réponse : elle cite volontiers
		// l'URL, et une URL de forge dans un message d'erreur finit dans un
		// ticket. Elle est journalisée côté serveur, où elle a sa place.
		slog.Error("rafraîchissement du miroir en échec",
			slog.String("repo", req.Repo), slog.String("ref", req.Ref),
			slog.String("error", err.Error()))
		repondreErreur(w, &gitwire.Error{
			Code:    gitwire.CodeMirrorFailed,
			Message: "la branche protégée n'a pas pu être rafraîchie depuis la forge",
		})
		return
	}

	// Le pointeur que le hook `pre-receive` lira comme ancre du contrôle de
	// fraîcheur. Un nom FIXE plutôt qu'un paramètre porté par chaque jeton : la
	// principale est une propriété du dépôt, elle ne varie pas d'un push à
	// l'autre, et deux sources pour un même fait finissent par diverger.
	//
	// Il vit sous `refs/warden/`, que le hook n'admet jamais en écriture — seul
	// `refs/audit/` l'est. Un client ne peut donc pas déplacer l'ancre.
	if err := depot.UpdateRef(r.Context(), gitMainlinePointer, sha); err != nil {
		slog.Error("pointeur de principale non écrit",
			slog.String("repo", req.Repo), slog.String("error", err.Error()))
		repondreErreur(w, &gitwire.Error{
			Code:    gitwire.CodeInternal,
			Message: "le miroir a été rafraîchi mais son pointeur n'a pas pu être écrit",
		})
		return
	}

	w.Header().Set("Content-Type", gitwire.ContentTypeError)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if err := json.NewEncoder(w).Encode(gitwire.MirrorResponse{SHA: sha.String()}); err != nil {
		slog.Error("réponse du miroir non écrite", slog.String("error", err.Error()))
	}
}

// handleResolve rend la position d'une ref nommée.
//
// Volontairement sans notion de run ni d'utilisateur : le rôle `api` compose la
// ref à partir de ce qu'il détient — l'identité authentifiée et l'identifiant du
// run — puis demande où elle pointe. C'est ce qui empêche un client de faire
// enregistrer comme sien le commit d'audit d'un autre.
func (s *Server) handleResolve(w http.ResponseWriter, r *http.Request) {
	var req gitwire.ResolveRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, tailleMaxRequete))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		repondreErreur(w, &gitwire.Error{
			Code:    gitwire.CodeBadRequest,
			Message: "corps de requête illisible",
		})
		return
	}

	ref, err := git.NewRef(req.Ref)
	if err != nil {
		repondreErreur(w, &gitwire.Error{Code: gitwire.CodeBadRequest, Message: "ref invalide"})
		return
	}

	dir, errDepot := s.depots.dir(req.Repo)
	if errDepot != nil {
		repondreErreur(w, errDepot)
		return
	}

	sha, err := git.New(dir).Resolve(r.Context(), ref.String())
	if err != nil {
		// Une ref absente n'est pas une panne : c'est un push qui n'a pas eu lieu,
		// et l'appelant doit pouvoir le dire à l'utilisateur plutôt que de rendre
		// une erreur interne.
		repondreErreur(w, &gitwire.Error{
			Code:    gitwire.CodeUnknownObject,
			Message: "cette ref n'existe pas dans ce dépôt",
		})
		return
	}

	w.Header().Set("Content-Type", gitwire.ContentTypeError)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if err := json.NewEncoder(w).Encode(gitwire.ResolveResponse{SHA: sha.String()}); err != nil {
		slog.Error("réponse de résolution non écrite", slog.String("error", err.Error()))
	}
}

// RunBranch est la ref que le runner voit dans son espace de noms (ADR-0007).
const RunBranch = "refs/heads/run"

// handleNamespace crée la ref d'espace de noms d'un run.
//
// Servi sur le listener interne, comme le reste : un pod runner qui pourrait
// appeler cet endpoint se créerait une ref vers l'objet de son choix, ce qui est
// exactement le premier vecteur de gitnamespaces(7).
func (s *Server) handleNamespace(w http.ResponseWriter, r *http.Request) {
	var req gitwire.NamespaceRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, tailleMaxRequete))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		repondreErreur(w, &gitwire.Error{Code: gitwire.CodeBadRequest, Message: "corps de requête illisible"})
		return
	}

	ns, err := git.NewNamespace(req.Namespace)
	if err != nil {
		repondreErreur(w, &gitwire.Error{Code: gitwire.CodeBadRequest, Message: "espace de noms invalide"})
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
	depot := git.New(dir)

	// L'objet doit exister AVANT que la ref le désigne. Une ref vers un objet
	// absent ne serait pas seulement inutile : elle ferait échouer le fetch du
	// runner avec un message parlant de dépôt corrompu, très loin de la cause.
	present, err := depot.HasObject(r.Context(), sha)
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

	// La composition passe par le socle : elle imbrique `refs/namespaces/` devant
	// CHAQUE composant, ce que la forme naïve ne fait pas — et son échec est
	// silencieux (ADR-0026).
	cible, err := git.NamespacedRef(ns, RunBranch)
	if err != nil {
		repondreErreur(w, &gitwire.Error{Code: gitwire.CodeBadRequest, Message: "espace de noms invalide"})
		return
	}

	if err := depot.UpdateRef(r.Context(), cible, sha); err != nil {
		slog.Error("création de la ref d'espace de noms en échec",
			slog.String("repo", req.Repo), slog.String("error", err.Error()))
		repondreErreur(w, &gitwire.Error{Code: gitwire.CodeInternal, Message: "la ref du run n'a pas pu être créée"})
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

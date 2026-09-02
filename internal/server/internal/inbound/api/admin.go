package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// Utilitaires partagés par la surface d'ADMINISTRATION.
//
// Ils vivent ici et non recopiés dans chaque handler pour une raison qui n'est
// pas l'économie de lignes : `adminOuRien` porte une décision de sécurité — un
// 404 plutôt qu'un 403 — et une décision de sécurité répétée à cinq endroits est
// une décision qu'un sixième endroit oubliera.

// adminOuRien refuse tout ce qui n'est pas administrateur, en NIANT la surface.
//
// 404 et non 403 : un 403 confirmerait l'existence de l'administration à un
// utilisateur ordinaire. Il n'a aucune raison de savoir qu'elle existe, et la
// liste des environnements comme celle des machines sont une carte de
// l'organisation — utile à qui cherche par où entrer.
func (a *Server) adminOuRien(w http.ResponseWriter, r *http.Request) bool {
	if !a.policy.CanAdministerRepos(r.Context(), ContextUser(r.Context())) {
		http.NotFound(w, r)
		return false
	}
	return true
}

// utilisateurID rend l'identifiant de l'appelant, 0 s'il n'y en a pas.
//
// Zéro devient NULL en base plutôt qu'une clé étrangère invalide : la trace dit
// alors « on ne sait pas qui », ce qui est vrai, au lieu de désigner quelqu'un.
func utilisateurID(r *http.Request) int64 {
	if u := ContextUser(r.Context()); u != nil {
		return u.ID
	}
	return 0
}

// decoderJSON lit un corps borné et strict, et répond lui-même en cas d'échec.
//
// `DisallowUnknownFields` : un champ que le serveur ne comprend pas est refusé
// plutôt qu'ignoré. Sur une surface de configuration, ignorer en silence est le
// chemin par lequel un opérateur croit avoir posé un réglage qui n'a jamais
// existé — une faute de frappe dans `host_key` désarmerait l'épinglage sans que
// rien ne le dise.
func decoderJSON(w http.ResponseWriter, r *http.Request, plafond int64, dest any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, plafond)

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dest); err != nil {
		// Le message de `encoding/json` nomme le champ fautif et ne cite pas les
		// valeurs, ce qui est exactement ce qu'il faut ici : ce corps peut
		// contenir une clé privée.
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

// ecrireJSON écrit la réponse.
//
// L'erreur d'écriture est journalisée et rien de plus : la réponse est déjà
// partie, et transformer ça en code d'erreur ferait croire à un échec de
// l'opération, qui a bien eu lieu.
func ecrireJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("réponse non écrite", slog.String("error", err.Error()))
	}
}

package api

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/jijins1/warden/internal/server/internal/core"
)

// hostPayload est LE FORMALISME de l'enrôlement, tel qu'il circule.
//
// Distinct de `core.ManagedHost` bien qu'il en soit le miroir, pour la raison
// déjà retenue pour les dépôts : le jour où l'un des deux gagne un champ que
// l'autre ne doit pas porter, la divergence se déclare ici plutôt que dans un
// type du cœur qui se mettrait à porter des étiquettes JSON.
//
// L'ADRESSE reste obligatoire — c'est là que l'enrôlement se connecte — mais
// elle accepte désormais un NOM autant qu'une adresse IP (ADR-0036). La
// résolution a alors lieu dans le pod, ce que l'ADR-0030 refusait ; ce qui
// protège de joindre la mauvaise machine est l'empreinte épinglée, pas la forme
// de ce champ.
type hostPayload struct {
	Name        string `json:"host"`
	Environment string `json:"environment,omitempty"`
	Address     string `json:"address"`
	Port        int    `json:"port,omitempty"`
	Login       string `json:"login"`

	// HostKey est l'empreinte épinglée. Facultative à la déclaration : une
	// machine dont on ne connaît pas encore l'empreinte est déclarable, et le
	// premier contact la capturera. La fournir ici est ce qui ÉVITE la
	// confiance à la première rencontre.
	HostKey string `json:"host_key,omitempty"`

	// Mode et EnrolledAt ne sont jamais lus d'une requête : le mode est décidé
	// par le chemin emprunté, et la date par la base.
	Mode       string `json:"mode,omitempty"`
	EnrolledAt string `json:"enrolled_at,omitempty"`
}

// declareHostsPayload est le document que `warden host declare` envoie.
//
// Une LISTE et une seule requête, pas une requête par machine : une flotte se
// déclare d'un fichier, et N requêtes laisseraient un état à moitié appliqué
// sur la première erreur.
type declareHostsPayload struct {
	Hosts []hostPayload `json:"hosts"`
}

// tailleMaxInventaire borne le document accepté. Une machine fait quelques
// centaines d'octets ; ce plafond en laisse passer plusieurs milliers.
const tailleMaxInventaire = 1 << 20

func versHostPayload(h core.ManagedHost) hostPayload {
	return hostPayload{
		Name:        h.Name,
		Environment: h.Environment,
		Address:     h.Address,
		Port:        h.Port,
		Login:       h.Login,
		HostKey:     h.HostKey,
		Mode:        string(h.Mode),
		EnrolledAt:  h.EnrolledAt.UTC().Format(formatDate),
	}
}

// versManagedHost traduit un descripteur reçu.
//
// Le MODE est imposé par l'appelant de cette fonction, jamais lu du corps : une
// requête qui pourrait se déclarer `assisted` ferait croire que warden s'est
// connecté à une machine qu'il n'a jamais vue.
func versManagedHost(p hostPayload, environnement string, mode core.EnrollmentMode) (core.ManagedHost, error) {
	// L'environnement du document l'emporte sur celui de la ligne quand il est
	// posé : un fichier `hosts.yaml` déclare le plus souvent un environnement,
	// et le répéter sur chaque machine est une occasion de se tromper.
	env := p.Environment
	if environnement != "" {
		env = environnement
	}

	port := p.Port
	if port == 0 {
		port = core.PortSSHParDefaut
	}

	h := core.ManagedHost{
		Name: p.Name, Environment: env, Address: p.Address,
		Port: port, Login: p.Login, HostKey: p.HostKey, Mode: mode,
	}
	return h, h.Validate()
}

// HandleListHosts rend l'inventaire des machines.
//
// GET /admin/hosts[?environment=prod]
func (a *Server) HandleListHosts(w http.ResponseWriter, r *http.Request) {
	if !a.adminOuRien(w, r) {
		return
	}
	if a.deps.Hosts == nil {
		http.Error(w, "host inventory is not available", http.StatusNotImplemented)
		return
	}

	hotes, err := a.deps.Hosts.ListHosts(r.Context(), r.URL.Query().Get("environment"))
	if err != nil {
		slog.Error("liste des machines en échec", slog.String("error", err.Error()))
		http.Error(w, "could not read the host inventory", http.StatusInternalServerError)
		return
	}

	out := make([]hostPayload, 0, len(hotes))
	for _, h := range hotes {
		out = append(out, versHostPayload(h))
	}
	ecrireJSON(w, out)
}

// pageDeMachinesPayload est la réponse de GET /hosts.
//
// Les machines gardent le formalisme de `GET /admin/hosts` : une machine est la
// même ligne quel que soit l'écran qui la montre.
type pageDeMachinesPayload struct {
	Hosts      []hostPayload `json:"hosts"`
	NextCursor string        `json:"next_cursor"`
}

// HandleListVisibleHosts rend les machines que l'appelant peut voir, paginées.
//
// GET /hosts?env=&q=&cursor=&limit=
//
// PAS derrière `adminOuRien`, contrairement à `GET /admin/hosts` : ce n'est
// pas l'inventaire de l'organisation mais la part qu'en voit l'appelant —
// les machines des environnements où il a un droit vivant (ADR-0052). Un
// administrateur sans droit n'y voit rien de plus, comme sous
// `GET /environments`.
func (a *Server) HandleListVisibleHosts(w http.ResponseWriter, r *http.Request) {
	user := ContextUser(r.Context())
	if user == nil {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if a.deps.Hosts == nil {
		http.Error(w, "host inventory is not available", http.StatusNotImplemented)
		return
	}

	// La portée n'est jamais lue de la requête : core.MachinesVisibles la pose
	// depuis l'appelant authentifié et ses droits vivants.
	criteres := core.CriteresDeMachines{
		Environment: r.URL.Query().Get("env"),
		Nom:         r.URL.Query().Get("q"),
		Cursor:      r.URL.Query().Get("cursor"),
	}
	// Une limite illisible prend le défaut, comme sous GET /runs : la borne
	// haute est tenue par le magasin.
	if limite, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && limite > 0 {
		criteres.Limit = limite
	}

	page, err := core.MachinesVisibles(r.Context(), a.deps.Hosts, a.deps.Access, user.ID, criteres)
	if errors.Is(err, core.ErrCurseurIllisible) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "liste des machines visibles en échec", slog.String("error", err.Error()))
		http.Error(w, "could not read the host inventory", http.StatusInternalServerError)
		return
	}

	out := pageDeMachinesPayload{Hosts: make([]hostPayload, 0, len(page.Hosts)), NextCursor: page.NextCursor}
	for _, h := range page.Hosts {
		out.Hosts = append(out.Hosts, versHostPayload(h))
	}
	ecrireJSON(w, out)
}

// HandleDeclareHosts enregistre des machines déclarées par un opérateur.
//
// PUT /admin/hosts[?enrollment=<run_ref>]
//
// Sans `enrollment`, c'est le MODE A : l'opérateur a posé la clé publique
// lui-même et déclare ce qu'il a fait. Warden n'a rien vérifié — il enregistre.
//
// Avec `enrollment`, c'est la seconde moitié du MODE B : un run d'enrôlement
// vient de poser la clé, et la CLI déclare la machine qu'il a jointe. Le mode
// devient `assisted` — mais SEULEMENT si le serveur peut le CONSTATER. Il
// vérifie que ce run existe, qu'il est bien un enrôlement, qu'il a RÉUSSI, et
// qu'il visait cette machine-là. Croire l'appelant sur parole ferait enregistrer
// « warden s'est connecté » à propos d'une machine qu'il n'a jamais vue.
//
// TOUT OU RIEN : le document entier est validé avant qu'une seule ligne parte en
// base. Un fichier à moitié appliqué laisserait un inventaire dont personne ne
// sait ce qu'il contient.
func (a *Server) HandleDeclareHosts(w http.ResponseWriter, r *http.Request) {
	if !a.adminOuRien(w, r) {
		return
	}
	if a.deps.Hosts == nil {
		http.Error(w, "host inventory is not available", http.StatusNotImplemented)
		return
	}

	var doc declareHostsPayload
	if !decoderJSON(w, r, tailleMaxInventaire, &doc) {
		return
	}
	if len(doc.Hosts) == 0 {
		http.Error(w, "no host in the document", http.StatusBadRequest)
		return
	}

	environnement := r.URL.Query().Get("environment")
	mode, cibleEnrolee, ok := a.modeDeclaration(w, r, environnement)
	if !ok {
		return
	}

	// Validation d'ABORD, écriture ensuite. Le message nomme la machine
	// fautive : sans lui, l'opérateur ne peut que relire son fichier en entier.
	hotes := make([]core.ManagedHost, 0, len(doc.Hosts))
	for _, p := range doc.Hosts {
		genre := mode
		if cibleEnrolee != "" && p.Name != cibleEnrolee {
			// Le run d'enrôlement ne couvre QU'UNE machine. Les autres lignes
			// du même document restent déclaratives : warden ne les a pas vues.
			genre = core.HostDeclared
		}
		h, err := versManagedHost(p, environnement, genre)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		h.EnrolledBy = utilisateurID(r)
		hotes = append(hotes, h)
	}

	for _, h := range hotes {
		if err := a.deps.Hosts.SaveHost(r.Context(), h); err != nil {
			slog.Error("déclaration de machine en échec",
				slog.String("hôte", h.Name), slog.String("error", err.Error()))
			http.Error(w, "could not record the host inventory", http.StatusInternalServerError)
			return
		}
	}

	slog.Info("machines déclarées",
		slog.Int("nombre", len(hotes)), slog.Int64("par", utilisateurID(r)))

	out := make([]hostPayload, 0, len(hotes))
	for _, h := range hotes {
		out = append(out, versHostPayload(h))
	}
	ecrireJSON(w, out)
}

// modeDeclaration décide si la déclaration peut se dire `assisted`.
//
// Elle rend le mode, le nom de la machine que le run d'enrôlement visait, et si
// la requête peut continuer. Le mode n'est JAMAIS lu du corps : il est déduit
// d'un fait que le serveur constate en base.
func (a *Server) modeDeclaration(w http.ResponseWriter, r *http.Request, environnement string) (core.EnrollmentMode, string, bool) {
	ref := r.URL.Query().Get("enrollment")
	if ref == "" {
		return core.HostDeclared, "", true
	}
	if a.deps.Runs == nil {
		http.Error(w, "run lookup is not available", http.StatusNotImplemented)
		return "", "", false
	}

	run, err := a.deps.Runs.RunByRef(r.Context(), ref)
	if err != nil {
		if errors.Is(err, core.ErrNotFound) {
			http.Error(w, "unknown enrollment "+ref, http.StatusBadRequest)
			return "", "", false
		}
		slog.Error("lecture du run d'enrôlement en échec", slog.String("error", err.Error()))
		http.Error(w, "could not verify the enrollment", http.StatusInternalServerError)
		return "", "", false
	}

	switch {
	case run.Kind != core.RunEnrollment:
		http.Error(w, "run "+ref+" is not an enrollment", http.StatusBadRequest)
		return "", "", false
	case run.State != core.RunSucceeded:
		// Un enrôlement qui a échoué n'établit rien. Déclarer sur sa foi
		// enregistrerait une machine que warden n'a pas réussi à joindre.
		http.Error(w, "enrollment "+ref+" did not succeed", http.StatusBadRequest)
		return "", "", false
	case run.EnrollTarget == nil:
		http.Error(w, "enrollment "+ref+" has no target", http.StatusBadRequest)
		return "", "", false
	case environnement != "" && run.Environment != environnement:
		// Un enrôlement de `staging` ne fait pas foi pour une machine de `prod`.
		http.Error(w, "enrollment "+ref+" belongs to another environment", http.StatusBadRequest)
		return "", "", false
	}
	return core.HostAssisted, run.EnrollTarget.Host, true
}

// HandleForgetHost retire une machine de l'inventaire.
//
// DELETE /admin/hosts/{environment}/{host}
//
// Elle ne retire RIEN de la machine : la clé publique reste dans son
// `authorized_keys`. Le produit ne peut pas prétendre le contraire, et l'entête
// de réponse le dit à qui automatise.
func (a *Server) HandleForgetHost(w http.ResponseWriter, r *http.Request) {
	if !a.adminOuRien(w, r) {
		return
	}
	if a.deps.Hosts == nil {
		http.Error(w, "host inventory is not available", http.StatusNotImplemented)
		return
	}

	env, nom := r.PathValue("environment"), r.PathValue("host")
	err := a.deps.Hosts.ForgetHost(r.Context(), env, nom)
	if errors.Is(err, core.ErrNoHost) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		slog.Error("retrait de machine en échec", slog.String("error", err.Error()))
		http.Error(w, "could not remove the host", http.StatusInternalServerError)
		return
	}

	slog.Info("machine retirée de l'inventaire",
		slog.String("environnement", env), slog.String("hôte", nom),
		slog.Int64("par", utilisateurID(r)),
		slog.String("note", "la clé publique reste dans son authorized_keys"))

	w.Header().Set("Warning", `299 - "the public key remains in the host authorized_keys"`)
	w.WriteHeader(http.StatusNoContent)
}

package api

import (
	"errors"
	"log/slog"
	"net/http"
	"net/netip"

	"github.com/jijins1/warden/internal/server/internal/core"
)

// hostPayload est LE FORMALISME de l'enrôlement, tel qu'il circule.
//
// Distinct de `core.ManagedHost` bien qu'il en soit le miroir, pour la raison
// déjà retenue pour les dépôts : le jour où l'un des deux gagne un champ que
// l'autre ne doit pas porter, la divergence se déclare ici plutôt que dans un
// type du cœur qui se mettrait à porter des étiquettes JSON.
//
// L'ADRESSE est obligatoire et ce n'est pas négociable : c'est là que
// l'enrôlement se connecte, et dépendre d'une résolution DNS faite dans le pod
// ferait enrôler une machine que l'opérateur croyait désigner.
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
		Address:     h.Address.String(),
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

	addr, err := netip.ParseAddr(p.Address)
	if err != nil {
		return core.ManagedHost{}, errAdresse(p)
	}

	port := p.Port
	if port == 0 {
		port = core.PortSSHParDefaut
	}

	h := core.ManagedHost{
		Name: p.Name, Environment: env, Address: addr,
		Port: port, Login: p.Login, HostKey: p.HostKey, Mode: mode,
	}
	return h, h.Validate()
}

func errAdresse(p hostPayload) error {
	return errorf("hôte %s : adresse %q illisible — une ADRESSE est attendue, "+
		"pas un nom : c'est là que l'enrôlement se connecte", p.Name, p.Address)
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

// HandleDeclareHosts enregistre des machines déclarées par un opérateur.
//
// PUT /admin/hosts
//
// C'est le MODE A de l'enrôlement : l'opérateur a posé la clé publique sur ses
// machines lui-même, et déclare ici ce qu'il a fait. Warden n'a rien vérifié —
// il enregistre. Le mode est donc `declared`, et la distinction compte : dans
// ce mode le produit n'a jamais vu de mot de passe.
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

	// Validation d'ABORD, écriture ensuite. Le message nomme la machine
	// fautive : sans lui, l'opérateur ne peut que relire son fichier en entier.
	hotes := make([]core.ManagedHost, 0, len(doc.Hosts))
	for _, p := range doc.Hosts {
		h, err := versManagedHost(p, r.URL.Query().Get("environment"), core.HostDeclared)
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

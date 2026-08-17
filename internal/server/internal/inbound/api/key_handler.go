package api

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/jijins1/warden/internal/server/internal/core"
)

// keyPayload est ce qu'on peut dire d'une clé sans la révéler.
//
// La partie PRIVÉE n'y figure pas et n'y figurera pas : rien dans le produit ne
// la rend, ni à un administrateur ni à personne. Elle descend vers le pod par le
// canal de dispatch, qui est un autre chemin (ADR-0018).
type keyPayload struct {
	Environment string `json:"environment"`
	// PublicKey est copiable telle quelle dans un `authorized_keys`. C'est sa
	// seule raison d'être.
	PublicKey   string `json:"public_key"`
	Fingerprint string `json:"fingerprint"`
	Origin      string `json:"origin"`
	CreatedAt   string `json:"created_at"`
	RotatedAt   string `json:"rotated_at,omitempty"`
}

func versKeyPayload(d core.KeyDescriptor) keyPayload {
	p := keyPayload{
		Environment: d.Environment,
		PublicKey:   d.PublicKey,
		Fingerprint: d.Fingerprint,
		Origin:      string(d.Origin),
		CreatedAt:   d.CreatedAt.UTC().Format(formatDate),
	}
	if !d.RotatedAt.IsZero() {
		p.RotatedAt = d.RotatedAt.UTC().Format(formatDate)
	}
	return p
}

const formatDate = "2006-01-02T15:04:05Z"

// putKeyPayload est ce qu'un administrateur envoie pour poser une clé.
//
// Les deux modes de l'enrôlement s'y distinguent, et ils sont EXCLUSIFS : soit
// l'opérateur fournit sa clé, soit warden la frappe. Accepter les deux
// obligerait à choisir, et un produit qui choisit silencieusement entre deux
// clés d'accès est un produit dont on ne sait pas ce qu'il a installé.
type putKeyPayload struct {
	// PrivateKey pose une clé existante (mode déclaratif). Elle DOIT être sans
	// phrase secrète : `ansible-runner` la charge par `ssh-add` avant de lancer
	// le playbook, et une clé protégée y attendrait une saisie qui ne viendra
	// jamais dans un pod.
	PrivateKey string `json:"private_key,omitempty"`

	// Generate demande à warden de frapper la paire. La partie privée ne quitte
	// alors jamais le cluster.
	Generate bool `json:"generate,omitempty"`
}

// tailleMaxCle borne le corps accepté. Une clé privée fait quelques
// kilo-octets ; au-delà, ce n'est pas une clé.
const tailleMaxCle = 64 << 10

// HandleListKeys rend les descripteurs de toutes les clés.
//
// GET /admin/keys
func (a *Server) HandleListKeys(w http.ResponseWriter, r *http.Request) {
	if !a.adminOuRien(w, r) {
		return
	}
	if a.deps.Keys == nil {
		http.Error(w, "key inventory is not available", http.StatusNotImplemented)
		return
	}

	cles, err := a.deps.Keys.ListKeys(r.Context())
	if err != nil {
		slog.Error("liste des clés en échec", slog.String("error", err.Error()))
		http.Error(w, "could not read the key inventory", http.StatusInternalServerError)
		return
	}

	out := make([]keyPayload, 0, len(cles))
	for _, d := range cles {
		out = append(out, versKeyPayload(d))
	}
	ecrireJSON(w, out)
}

// HandleGetKey rend le descripteur d'une clé.
//
// GET /admin/keys/{environment}
func (a *Server) HandleGetKey(w http.ResponseWriter, r *http.Request) {
	if !a.adminOuRien(w, r) {
		return
	}
	if a.deps.Keys == nil {
		http.Error(w, "key inventory is not available", http.StatusNotImplemented)
		return
	}

	d, err := a.deps.Keys.KeyByEnvironment(r.Context(), r.PathValue("environment"))
	if errors.Is(err, core.ErrNoKey) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		slog.Error("lecture de clé en échec", slog.String("error", err.Error()))
		http.Error(w, "could not read the key", http.StatusInternalServerError)
		return
	}
	ecrireJSON(w, versKeyPayload(d))
}

// HandlePutKey pose la clé d'un environnement.
//
// PUT /admin/keys/{environment}
//
// Poser une clé sur un environnement qui en a déjà une EST la rotation, et
// l'ADR-0020 note que c'est le seul mécanisme de révocation dont le produit
// dispose. La refuser obligerait à supprimer d'abord, donc à passer par un état
// où l'environnement n'a plus de clé.
func (a *Server) HandlePutKey(w http.ResponseWriter, r *http.Request) {
	if !a.adminOuRien(w, r) {
		return
	}
	if a.deps.KeyStore == nil {
		http.Error(w, "key inventory is not available", http.StatusNotImplemented)
		return
	}

	var p putKeyPayload
	if !decoderJSON(w, r, tailleMaxCle, &p) {
		return
	}

	// Exclusifs, et l'un des deux est obligatoire. Un corps vide poserait la
	// question « que voulait cet appel », et la seule réponse honnête est de la
	// renvoyer à l'appelant.
	switch {
	case p.Generate && p.PrivateKey != "":
		http.Error(w, "choose either generate or private_key, not both", http.StatusBadRequest)
		return
	case !p.Generate && p.PrivateKey == "":
		http.Error(w, "provide either generate or private_key", http.StatusBadRequest)
		return
	}

	env := r.PathValue("environment")
	var d core.KeyDescriptor
	var err error
	par := utilisateurID(r)
	if p.Generate {
		d, err = a.deps.KeyStore.Generate(r.Context(), env, par)
	} else {
		d, err = a.deps.KeyStore.Import(r.Context(), env, []byte(p.PrivateKey), par)
	}
	if err != nil {
		// Le message d'erreur est repris tel quel, et c'est délibéré : il dit
		// « clé protégée par une phrase secrète » ou « environnement refusé »,
		// c'est-à-dire ce que l'administrateur doit corriger. Aucun de ces
		// messages ne cite la clé.
		slog.Warn("pose de clé refusée",
			slog.String("environnement", env), slog.String("error", err.Error()))
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Qui a posé la clé d'un environnement décide de ce qui peut joindre la
	// production. La trace vaut celle d'un run.
	slog.Info("clé d'environnement posée",
		slog.String("environnement", env), slog.String("empreinte", d.Fingerprint),
		slog.String("origine", string(d.Origin)),
		slog.Int64("par", par))

	// PAS de WriteHeader ici : il figerait le statut avant qu'ecrireJSON pose
	// Content-Type, et l'entête serait silencieusement perdu. 200 est de toute
	// façon le défaut.
	ecrireJSON(w, versKeyPayload(d))
}

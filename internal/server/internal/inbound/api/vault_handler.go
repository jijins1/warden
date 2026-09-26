package api

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/jijins1/warden/internal/server/internal/core"
)

// vaultKeyPayload est ce qu'on peut dire d'une clé de vault sans la révéler.
//
// Aucun champ n'y porte la valeur en clair, et aucun n'y portera jamais : voir
// l'ADR-0045. C'est la différence avec `keyPayload`, qui porte une partie
// PUBLIQUE — un mot de passe symétrique n'en a pas.
type vaultKeyPayload struct {
	Environment string `json:"environment"`
	Fingerprint string `json:"fingerprint"`
	Origin      string `json:"origin"`
	CreatedAt   string `json:"created_at"`
	RotatedAt   string `json:"rotated_at,omitempty"`
}

func versVaultKeyPayload(d core.VaultKeyDescriptor) vaultKeyPayload {
	p := vaultKeyPayload{
		Environment: d.Environment,
		Fingerprint: d.Fingerprint,
		Origin:      string(d.Origin),
		CreatedAt:   d.CreatedAt.UTC().Format(formatDate),
	}
	if !d.RotatedAt.IsZero() {
		p.RotatedAt = d.RotatedAt.UTC().Format(formatDate)
	}
	return p
}

// putVaultKeyPayload est ce qu'on envoie pour poser la clé de vault d'un
// environnement.
//
// Les deux modes sont EXCLUSIFS, même contrainte que `putKeyPayload` : soit
// l'appelant fournit un mot de passe existant (migration d'un dépôt déjà
// chiffré), soit warden le tire.
type putVaultKeyPayload struct {
	Plaintext string `json:"plaintext,omitempty"`
	Generate  bool   `json:"generate,omitempty"`
}

// tailleMaxCleDeVault borne le corps accepté. Un mot de passe de vault ne
// dépasse jamais quelques centaines d'octets ; au-delà, ce n'est pas une clé.
const tailleMaxCleDeVault = 4 << 10

// encryptPayload est ce qu'on envoie pour chiffrer une valeur.
type encryptPayload struct {
	// Plaintext est la valeur à chiffrer. Elle transite une seule fois, dans
	// cette requête, et ne fait jamais l'aller-retour sous une autre forme
	// que le bloc chiffré rendu.
	Plaintext string `json:"plaintext"`
	// VarName nomme la variable dans le bloc `!vault |` rendu. Pas de valeur
	// par défaut : un nom absent produirait un bloc que l'appelant devrait
	// renommer à la main, ce qui est une occasion de mal coller un fragment.
	VarName string `json:"var_name"`
}

type encryptedPayload struct {
	// Block est le texte `nom: !vault |\n  ...` prêt à coller dans un fichier
	// YAML versionné.
	Block string `json:"block"`
}

// tailleMaxValeurAChiffrer borne le corps accepté. Une valeur de playbook ne
// dépasse jamais quelques kilo-octets.
const tailleMaxValeurAChiffrer = 64 << 10

// HandleGetVaultKey rend le descripteur de la clé de vault d'un environnement.
//
// GET /environments/{environment}/vault
func (a *Server) HandleGetVaultKey(w http.ResponseWriter, r *http.Request) {
	user := ContextUser(r.Context())
	env := r.PathValue("environment")
	if !a.autoriserLEnvironnement(w, r, user, env) {
		return
	}
	if a.deps.Vault == nil {
		http.Error(w, "vault inventory is not available", http.StatusNotImplemented)
		return
	}

	d, err := a.deps.Vault.Describe(r.Context(), env)
	if errors.Is(err, core.ErrNoVaultKey) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		slog.Error("lecture de clé de vault en échec", slog.String("error", err.Error()))
		http.Error(w, "could not read the vault key", http.StatusInternalServerError)
		return
	}
	ecrireJSON(w, versVaultKeyPayload(d))
}

// HandlePutVaultKey pose la clé de vault d'un environnement.
//
// POST /environments/{environment}/vault
//
// Poser une clé sur un environnement qui en a déjà une EST la rotation, sur
// le même principe que les clés SSH — et avec la même dette assumée que
// l'ADR-0045 documente : le contenu déjà chiffré du dépôt ne suit pas.
func (a *Server) HandlePutVaultKey(w http.ResponseWriter, r *http.Request) {
	user := ContextUser(r.Context())
	env := r.PathValue("environment")
	if !a.autoriserLEnvironnement(w, r, user, env) {
		return
	}
	if a.deps.Vault == nil {
		http.Error(w, "vault inventory is not available", http.StatusNotImplemented)
		return
	}

	var p putVaultKeyPayload
	if !decoderJSON(w, r, tailleMaxCleDeVault, &p) {
		return
	}

	switch {
	case p.Generate && p.Plaintext != "":
		http.Error(w, "choose either generate or plaintext, not both", http.StatusBadRequest)
		return
	case !p.Generate && p.Plaintext == "":
		http.Error(w, "provide either generate or plaintext", http.StatusBadRequest)
		return
	}

	var d core.VaultKeyDescriptor
	var err error
	par := utilisateurID(r)
	if p.Generate {
		d, err = a.deps.Vault.Generate(r.Context(), env, par)
	} else {
		d, err = a.deps.Vault.Import(r.Context(), env, []byte(p.Plaintext), par)
	}
	if err != nil {
		slog.Warn("pose de clé de vault refusée",
			slog.String("environnement", env), slog.String("error", err.Error()))
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	slog.Info("clé de vault posée",
		slog.String("environnement", env), slog.String("empreinte", d.Fingerprint),
		slog.String("origine", string(d.Origin)), slog.Int64("par", par))

	ecrireJSON(w, versVaultKeyPayload(d))
}

// HandleEncryptVault chiffre une valeur avec la clé de vault d'un
// environnement.
//
// POST /environments/{environment}/vault/encrypt
//
// C'est le SEUL endpoint par lequel un appelant interagit avec cette clé sans
// être son gestionnaire : ADR-0033 (droit d'accès à l'environnement) suffit,
// là où poser ou remplacer la clé demande le même droit — voir
// `autoriserLEnvironnement`, appelé ici comme sur les deux endpoints
// précédents.
func (a *Server) HandleEncryptVault(w http.ResponseWriter, r *http.Request) {
	user := ContextUser(r.Context())
	env := r.PathValue("environment")
	if !a.autoriserLEnvironnement(w, r, user, env) {
		return
	}
	if a.deps.Vault == nil {
		http.Error(w, "vault inventory is not available", http.StatusNotImplemented)
		return
	}

	var p encryptPayload
	if !decoderJSON(w, r, tailleMaxValeurAChiffrer, &p) {
		return
	}
	if p.VarName == "" {
		http.Error(w, "var_name is required", http.StatusBadRequest)
		return
	}

	bloc, err := a.deps.Vault.EncryptString(r.Context(), env, []byte(p.Plaintext), p.VarName)
	if errors.Is(err, core.ErrNoVaultKey) {
		http.Error(w, "environment "+env+" has no vault key", http.StatusBadRequest)
		return
	}
	if err != nil {
		// Le message n'est PAS recopié tel quel ici, contrairement à la pose de
		// clé : une erreur de chiffrement peut, selon la bibliothèque
		// sous-jacente, citer des fragments de son entrée.
		slog.Error("chiffrement de vault en échec",
			slog.String("environnement", env), slog.String("error", err.Error()))
		http.Error(w, "could not encrypt the value", http.StatusInternalServerError)
		return
	}
	ecrireJSON(w, encryptedPayload{Block: bloc})
}

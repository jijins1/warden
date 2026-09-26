package admin

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/jijins1/warden/internal/cli/internal/client"
	"github.com/jijins1/warden/internal/cli/internal/prompt"
)

// OptionsCleDeVault décrit ce qu'on demande à `warden vault`.
type OptionsCleDeVault struct {
	Environment string

	// Generate demande au serveur de tirer le mot de passe. Il ne quitte
	// alors jamais le cluster, ce qui en fait le chemin à préférer.
	Generate bool

	// Cle désigne un mot de passe EXISTANT à importer — migration d'un dépôt
	// déjà chiffré ailleurs. Exclusif avec Generate.
	Cle *prompt.Source
}

// PoserCleDeVault importe ou tire la clé de vault d'un environnement.
//
// Contrairement à `warden key import` — une clé PEM, multiligne, dont la
// saisie au terminal est refusée — un mot de passe de vault est une valeur
// COURTE, sur le même patron que le mot de passe d'un enrôlement : `o.Cle`
// vient de `prompt.Declarer` et non `DeclarerMultiligne`, donc `Lire` invite
// au terminal quand ni fichier ni entrée standard n'ont été donnés.
func PoserCleDeVault(ctx context.Context, api *client.Client, o OptionsCleDeVault, sortie io.Writer) error {
	if o.Environment == "" {
		return fmt.Errorf("--env est requis")
	}
	if o.Generate && o.Cle != nil && o.Cle.Fournie() {
		return errors.New("--generate et l'import d'un mot de passe existant s'excluent")
	}

	p := client.PutVaultKey{Generate: o.Generate}
	if !o.Generate {
		if o.Cle == nil {
			return errors.New("fournir un mot de passe à importer, ou --generate")
		}
		secret, err := o.Cle.Lire(ctx)
		if err != nil {
			return err
		}
		defer secret.Wipe()
		p.Plaintext = string(secret.Reveal())
	}

	cle, err := api.SetVaultKey(ctx, o.Environment, p)
	if err != nil {
		return err
	}

	afficherCleDeVault(sortie, cle)
	return nil
}

// MontrerCleDeVault affiche le descripteur de la clé de vault d'un
// environnement.
func MontrerCleDeVault(ctx context.Context, api *client.Client, environnement string, sortie io.Writer) error {
	if environnement == "" {
		return fmt.Errorf("--env est requis")
	}
	cle, err := api.GetVaultKey(ctx, environnement)
	if err != nil {
		return err
	}
	afficherCleDeVault(sortie, cle)
	return nil
}

// afficherCleDeVault rend le descripteur, empreinte en tête — même
// convention que `afficherCle` pour les clés SSH.
func afficherCleDeVault(w io.Writer, c client.VaultKey) {
	_, _ = fmt.Fprintf(w, "%s\n", c.Environment)
	_, _ = fmt.Fprintf(w, "  empreinte  %s\n", c.Fingerprint)
	_, _ = fmt.Fprintf(w, "  origine    %s\n", origineLisible(c.Origin))
	_, _ = fmt.Fprintf(w, "  posée le   %s\n", c.CreatedAt)
	if c.RotatedAt != "" {
		_, _ = fmt.Fprintf(w, "  tournée le %s\n", c.RotatedAt)
	}
}

// ChiffrerPourVault chiffre une valeur avec la clé de vault d'un
// environnement et affiche le bloc `!vault |` prêt à coller.
//
// `valeur` invite au terminal par défaut, comme le mot de passe d'une
// session : c'est la façon la plus sûre de saisir un secret court qu'on ne
// veut voir nulle part dans un historique de shell.
func ChiffrerPourVault(ctx context.Context, api *client.Client, environnement, nomVariable string, valeur *prompt.Source, sortie io.Writer) error {
	if environnement == "" {
		return fmt.Errorf("--env est requis")
	}
	if nomVariable == "" {
		return fmt.Errorf("--var est requis")
	}

	secret, err := valeur.Lire(ctx)
	if err != nil {
		return err
	}
	defer secret.Wipe()

	bloc, err := api.EncryptVault(ctx, environnement, nomVariable, string(secret.Reveal()))
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintln(sortie, bloc)
	return nil
}

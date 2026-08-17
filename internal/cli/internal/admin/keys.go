// Package admin porte les commandes de configuration : clés d'accès et
// machines enrôlées.
//
// Séparé de `submit` parce que les deux s'adressent à des gens différents —
// l'un à qui lance des runs, l'autre à qui tient la flotte — et que le second
// manipule des clés privées.
package admin

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/jijins1/warden/internal/cli/internal/client"
	"github.com/jijins1/warden/internal/cli/internal/prompt"
)

// OptionsCle décrit ce qu'on demande à `warden key`.
type OptionsCle struct {
	Environment string

	// Generate demande au serveur de frapper la paire. La partie privée ne
	// quitte alors jamais le cluster, ce qui en fait le chemin à préférer.
	Generate bool

	// KeyFile et KeyStdin désignent une clé EXISTANTE à importer. Exclusifs
	// entre eux et avec Generate.
	KeyFile  string
	KeyStdin bool
}

// PoserCle importe ou frappe la clé d'un environnement.
//
// Le chemin d'import n'est jamais implicite : sans `--key-file` ni
// `--key-stdin`, la commande échoue au lieu de lire l'entrée standard. Un
// `warden key import < inventaire.yml` qui prendrait la première ligne pour une
// clé serait accepté par personne et découvert par tout le monde.
func PoserCle(ctx context.Context, api *client.Client, o OptionsCle, sortie io.Writer) error {
	if o.Environment == "" {
		return fmt.Errorf("--env est requis")
	}

	nb := 0
	for _, choisi := range []bool{o.Generate, o.KeyFile != "", o.KeyStdin} {
		if choisi {
			nb++
		}
	}
	switch nb {
	case 1:
	case 0:
		return fmt.Errorf("choisir --generate, --key-file <chemin> ou --key-stdin")
	default:
		return fmt.Errorf("--generate, --key-file et --key-stdin s'excluent")
	}

	p := client.PutKey{Generate: o.Generate}
	if !o.Generate {
		secret, err := lireCle(o)
		if err != nil {
			return err
		}
		// Effacée dès que le corps est composé. Effet partiel — le ramasse-miettes
		// a pu recopier — mais la fenêtre est plus courte que si on ne faisait
		// rien.
		defer secret.Wipe()
		p.PrivateKey = string(secret.Reveal())
	}

	cle, err := api.SetKey(ctx, o.Environment, p)
	if err != nil {
		return err
	}

	afficherCle(sortie, cle)
	_, _ = fmt.Fprintf(sortie, "\nÀ poser dans l'authorized_keys de chaque machine de « %s » :\n\n  %s\n",
		o.Environment, cle.PublicKey)
	return nil
}

// lireCle lit la partie privée, jamais depuis argv.
//
// La ligne de commande d'un processus est lisible par tout utilisateur de la
// machine : il n'y a donc pas de `--key <valeur>`, et il n'y en aura pas.
func lireCle(o OptionsCle) (prompt.Secret, error) {
	if o.KeyStdin {
		return prompt.ReadAll(os.Stdin)
	}
	f, err := os.Open(o.KeyFile) // #nosec G304 -- chemin fourni par l'opérateur, c'est l'objet de la commande
	if err != nil {
		return nil, fmt.Errorf("lecture de la clé : %w", err)
	}
	defer func() { _ = f.Close() }()
	return prompt.ReadAll(f)
}

// MontrerCle affiche le descripteur d'un environnement.
func MontrerCle(ctx context.Context, api *client.Client, environnement string, sortie io.Writer) error {
	if environnement == "" {
		return fmt.Errorf("--env est requis")
	}
	cle, err := api.GetKey(ctx, environnement)
	if err != nil {
		return err
	}
	afficherCle(sortie, cle)
	_, _ = fmt.Fprintf(sortie, "\n%s\n", cle.PublicKey)
	return nil
}

// ListerCles affiche toutes les clés.
func ListerCles(ctx context.Context, api *client.Client, sortie io.Writer) error {
	cles, err := api.ListKeys(ctx)
	if err != nil {
		return err
	}
	if len(cles) == 0 {
		_, _ = fmt.Fprintln(sortie, "aucune clé posée")
		return nil
	}
	for i, c := range cles {
		if i > 0 {
			_, _ = fmt.Fprintln(sortie)
		}
		afficherCle(sortie, c)
	}
	return nil
}

// afficherCle rend le descripteur, empreinte en tête.
//
// L'empreinte d'abord parce que c'est ce qu'un humain COMPARE — avec la sortie
// de `ssh-keygen -l` sur une machine, ou avec `ssh-add -l` dans un run.
func afficherCle(w io.Writer, c client.Key) {
	_, _ = fmt.Fprintf(w, "%s\n", c.Environment)
	_, _ = fmt.Fprintf(w, "  empreinte  %s\n", c.Fingerprint)
	_, _ = fmt.Fprintf(w, "  origine    %s\n", origineLisible(c.Origin))
	_, _ = fmt.Fprintf(w, "  posée le   %s\n", c.CreatedAt)
	if c.RotatedAt != "" {
		_, _ = fmt.Fprintf(w, "  tournée le %s\n", c.RotatedAt)
	}
}

// origineLisible dit ce que l'origine implique, pas seulement ce qu'elle vaut.
//
// La distinction porte une conséquence de sécurité : une clé importée a existé
// ailleurs, ce qui change jusqu'où remonte une compromission.
func origineLisible(o string) string {
	switch o {
	case "generated":
		return "frappée par warden — la partie privée n'a jamais quitté le cluster"
	case "imported":
		return "importée — la partie privée a existé hors du cluster"
	default:
		return o
	}
}

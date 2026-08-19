// Package admin porte les commandes de configuration : clés d'accès et
// machines enrôlées.
//
// Séparé de `submit` parce que les deux s'adressent à des gens différents —
// l'un à qui lance des runs, l'autre à qui tient la flotte — et que le second
// manipule des clés privées.
package admin

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/jijins1/warden/internal/cli/internal/client"
	"github.com/jijins1/warden/internal/cli/internal/prompt"
)

// OptionsCle décrit ce qu'on demande à `warden key`.
type OptionsCle struct {
	Environment string

	// Generate demande au serveur de frapper la paire. La partie privée ne
	// quitte alors jamais le cluster, ce qui en fait le chemin à préférer.
	Generate bool

	// Cle désigne une clé EXISTANTE à importer, et d'où elle vient. Exclusive
	// avec Generate.
	//
	// Un `*prompt.Source` et non trois champs : la décision « fichier, entrée
	// standard, ou invite » est la même pour toutes les valeurs sensibles du
	// produit — le mot de passe d'un enrôlement, celui de `become` demain — et
	// elle est portée une fois.
	Cle *prompt.Source
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

	fournie := o.Cle != nil && o.Cle.Fournie()
	switch {
	case o.Generate && fournie:
		return errors.New("--generate et l'import d'une clé existante s'excluent")
	case !o.Generate && !fournie:
		return errors.New("choisir --generate, --key-file <chemin> ou --key-stdin")
	}

	p := client.PutKey{Generate: o.Generate}
	if !o.Generate {
		secret, err := o.Cle.Lire(ctx)
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

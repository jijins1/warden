package admin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"sigs.k8s.io/yaml"

	"github.com/jijins1/warden/internal/cli/internal/client"
)

// tailleMaxDocument borne le fichier de déclaration accepté.
//
// Une machine fait quelques centaines d'octets ; ce plafond en laisse passer
// plusieurs milliers. Il existe pour qu'un chemin erroné — un dump SQL, une
// image — échoue en le disant plutôt qu'en remplissant la mémoire.
const tailleMaxDocument = 1 << 20

// DeclarerHotes enregistre les machines d'un fichier de déclaration.
//
// C'est le MODE A de l'enrôlement : l'opérateur a posé la clé publique sur ses
// machines lui-même, et déclare ici ce qu'il a fait. Warden n'a rien vérifié — il
// enregistre, et le mode `declared` le dit.
//
// Le document est lu et validé LOCALEMENT avant d'être envoyé : une erreur de
// forme se rapporte alors avec le fichier sous les yeux, pas après un aller-retour
// réseau.
func DeclarerHotes(ctx context.Context, api *client.Client, chemin, environnement string, sortie io.Writer) error {
	if chemin == "" {
		return errors.New("-f <fichier> est requis")
	}

	inv, err := lireInventaire(chemin)
	if err != nil {
		return err
	}
	if len(inv.Hosts) == 0 {
		return fmt.Errorf("%s ne déclare aucune machine", chemin)
	}
	if err := verifier(inv, environnement); err != nil {
		return err
	}

	hotes, err := api.DeclareHosts(ctx, environnement, inv)
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintf(sortie, "%d machine(s) enrôlée(s)\n\n", len(hotes))
	afficherHotes(sortie, hotes)

	// Ce que la déclaration NE FAIT PAS. Sans cette phrase, un opérateur peut
	// croire que warden a posé la clé, alors qu'il vient seulement d'enregistrer
	// qu'elle y est.
	_, _ = fmt.Fprintf(sortie, "\nRappel : warden n'a rien posé sur ces machines. "+
		"Leur authorized_keys doit déjà contenir la clé de l'environnement\n"+
		"(`warden key show --env <nom>` l'affiche).\n")
	return nil
}

// lireInventaire lit le document, en YAML ou en JSON.
//
// `sigs.k8s.io/yaml` convertit le YAML en JSON puis délègue à `encoding/json` :
// une seule structure, des étiquettes `json` qui font autorité, et le même
// refus des champs inconnus des deux côtés. Deux analyseurs auraient fini par
// accepter des choses différentes.
func lireInventaire(chemin string) (client.Inventory, error) {
	f, err := os.Open(chemin) // #nosec G304 -- chemin fourni par l'opérateur, c'est l'objet de la commande
	if err != nil {
		return client.Inventory{}, fmt.Errorf("lecture de %s : %w", chemin, err)
	}
	defer func() { _ = f.Close() }()

	brut, err := io.ReadAll(io.LimitReader(f, tailleMaxDocument+1))
	if err != nil {
		return client.Inventory{}, fmt.Errorf("lecture de %s : %w", chemin, err)
	}
	if len(brut) > tailleMaxDocument {
		return client.Inventory{}, fmt.Errorf("%s dépasse %d octets — est-ce bien un fichier de déclaration ?",
			chemin, tailleMaxDocument)
	}

	var inv client.Inventory
	// Strict : une clé mal orthographiée — `hostkey` au lieu de `host_key` —
	// désarmerait l'épinglage de l'empreinte sans que rien ne le dise.
	if err := yaml.UnmarshalStrict(brut, &inv); err != nil {
		return client.Inventory{}, fmt.Errorf("%s : %w", chemin, err)
	}
	return inv, nil
}

// verifier applique localement ce que le serveur exigera.
//
// Le doublon avec la validation serveur est VOULU : celle-ci a le fichier et
// peut nommer la ligne fautive, celle-là a la base et fait autorité. La
// première existe pour la personne, la seconde pour la correction.
func verifier(inv client.Inventory, environnement string) error {
	vus := map[string]int{}
	for i, h := range inv.Hosts {
		rang := i + 1
		switch {
		case h.Name == "":
			return fmt.Errorf("machine n°%d : `host` manquant", rang)
		case h.Address == "":
			return fmt.Errorf("machine %s : `address` manquante — c'est là que "+
				"l'enrôlement se connecte. Une adresse IP, ou un nom que LE POD "+
				"sait résoudre : sa vue du DNS n'est pas la vôtre, et il ne lit "+
				"pas votre ssh_config", h.Name)
		case h.Login == "":
			return fmt.Errorf("machine %s : `login` manquant — quel compte warden doit-il ouvrir ?", h.Name)
		case h.Environment == "" && environnement == "":
			return fmt.Errorf("machine %s : `environment` manquant, et --env n'est pas donné", h.Name)
		}

		cle := h.Environment + "/" + h.Name
		if environnement != "" {
			cle = environnement + "/" + h.Name
		}
		if precedent, deja := vus[cle]; deja {
			// Deux fois la même machine dans un document, ce sont deux réponses à
			// la même question : la seconde écraserait la première en silence.
			return fmt.Errorf("machine %s déclarée deux fois (lignes n°%d et n°%d)",
				cle, precedent, rang)
		}
		vus[cle] = rang
	}
	return nil
}

// ListerHotes affiche l'inventaire.
func ListerHotes(ctx context.Context, api *client.Client, environnement string, sortie io.Writer) error {
	hotes, err := api.ListHosts(ctx, environnement)
	if err != nil {
		return err
	}
	if len(hotes) == 0 {
		_, _ = fmt.Fprintln(sortie, "aucune machine enrôlée")
		return nil
	}
	afficherHotes(sortie, hotes)
	return nil
}

// OublierHote retire une machine de l'inventaire.
func OublierHote(ctx context.Context, api *client.Client, environnement, nom string, sortie io.Writer) error {
	if environnement == "" || nom == "" {
		return errors.New("--env et --host sont requis")
	}
	if err := api.ForgetHost(ctx, environnement, nom); err != nil {
		return err
	}

	// La limite est dite à chaque fois, pas une seule fois dans une
	// documentation : le produit ne peut pas retirer une clé d'une machine, et
	// laisser croire à une révocation serait le pire des silences.
	_, _ = fmt.Fprintf(sortie, "%s/%s retirée de l'inventaire.\n\n"+
		"ATTENTION : la clé publique reste dans son authorized_keys. warden ne\n"+
		"l'en a pas retirée et ne sait pas le faire — cette machine reste\n"+
		"joignable par la clé de « %s » tant que vous ne l'aurez pas fait.\n",
		environnement, nom, environnement)
	return nil
}

// afficherHotes rend l'inventaire en colonnes alignées.
//
// L'empreinte est réduite à sa présence : la ligne complète ferait dix fois la
// largeur du terminal, et ce qu'on veut voir d'un coup d'œil est QUELLES
// machines sont épinglées et lesquelles ne le sont pas.
func afficherHotes(w io.Writer, hotes []client.Host) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ENVIRONNEMENT\tMACHINE\tADRESSE\tCOMPTE\tMODE\tEMPREINTE")
	for _, h := range hotes {
		port := ""
		if h.Port != 0 && h.Port != 22 {
			port = fmt.Sprintf(":%d", h.Port)
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s%s\t%s\t%s\t%s\n",
			h.Environment, h.Name, h.Address, port, h.Login,
			modeLisible(h.Mode), etatEmpreinte(h.HostKey))
	}
	_ = tw.Flush()
}

func modeLisible(m string) string {
	switch m {
	case "declared":
		return "déclarée"
	case "assisted":
		return "enrôlée"
	default:
		return m
	}
}

// etatEmpreinte dit si la machine est épinglée, sans afficher la clé.
//
// Une machine sans empreinte n'est pas protégée contre un intermédiaire : le
// premier contact fera confiance à ce qui répond. Ça doit se voir dans la
// liste, pas seulement dans une documentation.
func etatEmpreinte(cle string) string {
	if cle == "" {
		return "AUCUNE"
	}
	return "épinglée"
}

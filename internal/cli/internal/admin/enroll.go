package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/jijins1/warden/internal/cli/internal/client"
	"github.com/jijins1/warden/internal/cli/internal/prompt"
	"github.com/jijins1/warden/internal/cli/internal/stream"
	"github.com/jijins1/warden/internal/cli/internal/upload"
	"github.com/jijins1/warden/internal/events"
	"github.com/jijins1/warden/internal/wire/ws"
)

// OptionsEnrolement décrit ce qu'on demande à `warden enroll`.
type OptionsEnrolement struct {
	Environment string
	Host        string
	Address     string
	Port        int
	Login       string

	// MotDePasse dit d'où vient le mot de passe du compte. Sans provenance
	// explicite, la saisie au terminal est prise par défaut — c'est le geste
	// attendu de cette commande.
	MotDePasse *prompt.Source

	// RunRef permet de reprendre un enrôlement interrompu.
	RunRef string

	// Confirmer est appelée avec l'empreinte constatée avant que la machine
	// soit déclarée. Rendre false abandonne la déclaration.
	//
	// C'est LA revue humaine du premier contact : sans empreinte déclarée à
	// l'avance, l'enrôlement fait confiance à ce qui répond, et ce moment est le
	// seul où quelqu'un peut encore dire non.
	Confirmer func(empreinte string) bool
}

// Suiveur suit le déroulement d'un run. Déclaré ici, du côté qui l'appelle.
type Suiveur interface {
	Suivre(ctx context.Context, runRef string, voir func(ws.EventFrame)) (stream.Fin, error)
}

// Enroler pose la clé de l'environnement sur une machine, et la déclare.
//
// Cinq temps, et l'ordre porte la correction :
//
//  1. le mot de passe est SAISI, jamais reçu d'argv ;
//  2. un run d'enrôlement est déclaré et démarré côté serveur ;
//  3. son déroulement est suivi et affiché — c'est de l'audit, pas une
//     barre de progression ;
//  4. l'empreinte constatée est SOUMISE à l'utilisateur ;
//  5. la machine n'est déclarée qu'après son accord, et par la CLI — le pod ne
//     touche jamais à l'inventaire.
func Enroler(ctx context.Context, api *client.Client, suiveur Suiveur, o OptionsEnrolement, sortie io.Writer) error {
	if err := o.valider(); err != nil {
		return err
	}

	motDePasse, err := o.MotDePasse.Lire(ctx)
	if err != nil {
		return err
	}
	defer motDePasse.Wipe()

	runRef := o.RunRef
	if runRef == "" {
		// Le MÊME générateur que `warden submit` : deux formes d'identifiant
		// dans le même produit finiraient par diverger, et c'est celui-ci que le
		// schéma contraint.
		runRef, err = upload.NewRunRef()
		if err != nil {
			return err
		}
	}

	port := o.Port
	if port == 0 {
		port = 22
	}

	_, err = api.CreateEnrollment(ctx, client.NewEnrollment{
		RunRef: runRef, Environment: o.Environment,
		Host: o.Host, Address: o.Address, Port: port, Login: o.Login,
		Password: string(motDePasse.Reveal()),
	})
	if err != nil {
		return err
	}
	// Effacé dès que la requête est partie : la valeur a fait son voyage.
	motDePasse.Wipe()

	_, _ = fmt.Fprintf(sortie, "enrôlement %s\n\n", runRef)

	res, fin, err := suivreLEnrolement(ctx, suiveur, runRef, sortie)
	if err != nil {
		return err
	}

	if fin.Status != "succeeded" {
		// L'empreinte est affichée MÊME en échec quand on l'a vue : un
		// enrôlement qui échoue après avoir constaté l'empreinte a quand même
		// appris quelque chose, et c'est ce qu'il faut déclarer pour épingler.
		if res.Fingerprint != "" {
			_, _ = fmt.Fprintf(sortie, "\nempreinte constatée : %s\n", res.Fingerprint)
		}
		return fmt.Errorf("l'enrôlement de %s a échoué (%s)", o.Host, fin.Status)
	}
	if res.HostKey == "" {
		// Le run dit avoir réussi mais n'a pas rapporté d'empreinte. Déclarer la
		// machine sans elle la laisserait non épinglée en la croyant enrôlée.
		return fmt.Errorf("l'enrôlement de %s s'est terminé sans rapporter d'empreinte", o.Host)
	}

	_, _ = fmt.Fprintf(sortie, "\nempreinte de %s :\n  %s\n", o.Host, res.Fingerprint)
	if o.Confirmer != nil && !o.Confirmer(res.Fingerprint) {
		_, _ = fmt.Fprintf(sortie, "\nDéclaration abandonnée. La clé EST posée sur %s ;\n"+
			"la machine n'est simplement pas enregistrée dans l'inventaire.\n", o.Host)
		return errors.New("déclaration refusée")
	}

	if _, err := api.DeclareHosts(ctx, o.Environment, client.Inventory{Hosts: []client.Host{{
		Name: o.Host, Environment: o.Environment, Address: o.Address,
		Port: port, Login: o.Login, HostKey: res.HostKey,
	}}}); err != nil {
		return fmt.Errorf("la machine est enrôlée mais n'a pas pu être déclarée : %w", err)
	}

	_, _ = fmt.Fprintf(sortie, "\n%s est enrôlée et épinglée.\n", o.Host)
	return nil
}

func (o OptionsEnrolement) valider() error {
	switch {
	case o.Environment == "":
		return errors.New("--env est requis")
	case o.Host == "":
		return errors.New("--host est requis")
	case o.Address == "":
		return errors.New("--address est requis — une ADRESSE, pas un nom : " +
			"c'est là que l'enrôlement se connecte")
	case o.Login == "":
		return errors.New("--login est requis : quel compte warden doit-il ouvrir ?")
	case o.MotDePasse == nil:
		return errors.New("aucune provenance pour le mot de passe")
	}
	return nil
}

// resultatEnrolement est ce que le pod rapporte de la machine.
type resultatEnrolement struct {
	HostKey      string `json:"warden_host_key"`
	Fingerprint  string `json:"warden_fingerprint"`
	DejaPresente bool   `json:"warden_deja_presente"`
}

// suivreLEnrolement affiche le déroulement et retient l'empreinte.
//
// Le récit est lu depuis les PAYLOADS et non depuis un format d'affichage : le
// pod émet une structure, et faire découper du français à la CLI casserait à la
// première reformulation.
func suivreLEnrolement(ctx context.Context, suiveur Suiveur, runRef string, sortie io.Writer) (resultatEnrolement, stream.Fin, error) {
	var res resultatEnrolement

	fin, err := suiveur.Suivre(ctx, runRef, func(e ws.EventFrame) {
		// L'empreinte d'abord : c'est la seule donnée qu'on retient.
		var r resultatEnrolement
		if err := json.Unmarshal(e.Payload, &r); err == nil && r.HostKey != "" {
			res = r
		}

		var etape struct {
			Texte string `json:"warden_enrolement"`
		}
		if err := json.Unmarshal(e.Payload, &etape); err == nil && etape.Texte != "" {
			_, _ = fmt.Fprintf(sortie, "  %s %s\n", marqueur(e.State), etape.Texte)
		}
	})
	if err != nil {
		return res, stream.Fin{}, err
	}
	return res, fin, nil
}

// marqueur rend l'état d'une étape en un caractère.
//
// `unreachable` porte ici un sens précis et grave : la machine jointe n'est pas
// celle qu'on visait. Le distinguer d'un échec ordinaire est ce qui fait qu'on
// enquête au lieu de réessayer.
func marqueur(etat events.State) string {
	switch etat {
	case events.StateFailed:
		return "✗"
	case events.StateUnreachable:
		return "⚠"
	case events.StateChanged:
		return "+"
	default:
		return "·"
	}
}

// ConfirmerAuTerminal demande l'accord de l'utilisateur sur une empreinte.
//
// Le refus est le DÉFAUT quand la réponse n'est pas un oui franc : accepter une
// empreinte par distraction est exactement ce que cette question existe pour
// empêcher.
func ConfirmerAuTerminal(entree io.Reader, sortie io.Writer) func(string) bool {
	return func(empreinte string) bool {
		_, _ = fmt.Fprintf(sortie, "\nCette empreinte est-elle celle de la machine attendue ?\n"+
			"Comparez-la avec `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub` sur la machine.\n"+
			"Déclarer et épingler ? [o/N] ")

		reponse, err := prompt.ReadLine(entree)
		if err != nil {
			return false
		}
		switch strings.ToLower(strings.TrimSpace(string(reponse.Reveal()))) {
		case "o", "oui", "y", "yes":
			return true
		default:
			return false
		}
	}
}

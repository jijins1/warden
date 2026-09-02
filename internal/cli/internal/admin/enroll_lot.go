package admin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/jijins1/warden/internal/cli/internal/ansible"
	"github.com/jijins1/warden/internal/cli/internal/client"
	"github.com/jijins1/warden/internal/cli/internal/prompt"
)

// OptionsLot décrit ce qu'on demande à `warden enroll --all`.
type OptionsLot struct {
	OptionsInventaire

	// MotDePasse dit d'où vient le mot de passe des comptes.
	MotDePasse *prompt.Source

	// ParMachine demande un mot de passe pour CHAQUE machine, au lieu d'un seul
	// réutilisé. Pour une flotte dont les comptes ne partagent pas le leur.
	ParMachine bool

	// AccepterNouvelles saute la confirmation d'empreinte de chaque machine, en
	// laissant `ConfirmerEmpreinte` nulle.
	//
	// Le nom est long exprès. C'est la seule revue humaine du premier contact
	// (ADR-0030), et un drapeau qui la désarme doit se lire dans un historique de
	// shell sans qu'on ait à se demander ce qu'il fait. Les empreintes constatées
	// figurent alors au récapitulatif, pour qu'elles restent vérifiables après
	// coup.
	AccepterNouvelles bool

	// ArreterAuPremierEchec interrompt le lot au premier échec. Par défaut le lot
	// continue : sur une flotte, l'échec d'une machine ne dit rien des autres, et
	// s'arrêter obligerait à relancer autant de fois qu'il y a de problèmes.
	ArreterAuPremierEchec bool

	// Confirmer est appelée avec le plan avant la première connexion.
	Confirmer func([]ansible.Machine) bool

	// ConfirmerEmpreinte est la revue humaine du premier contact, posée pour
	// chaque machine. Injectée plutôt que construite ici : ce paquet ne connaît
	// pas le terminal, et c'est ce qui rend le lot vérifiable sans en avoir un.
	ConfirmerEmpreinte func(Empreinte) bool
}

// resultat est ce qu'on retient d'une machine du lot.
type resultat struct {
	Machine   ansible.Machine
	Empreinte Empreinte
	Err       error
}

// EnrolerLot pose la clé sur toutes les machines de l'inventaire qui ne sont pas
// encore enrôlées.
//
// SÉQUENTIEL, une machine à la fois, et ce n'est pas une simplification :
// l'enrôlement est un RUN, et le schéma lie un run à une cible unique
// (`run.enroll_target`). Le serveur ne change pas d'un iota pour cette commande —
// elle fait N fois ce que `warden enroll` fait une fois, en épargnant à
// l'opérateur les N invocations et les N saisies.
func EnrolerLot(ctx context.Context, api *client.Client, suiveur Suiveur, o OptionsLot, sortie io.Writer) error {
	if o.MotDePasse == nil {
		return errors.New("aucune provenance pour le mot de passe")
	}

	plan, err := PreparerLot(ctx, api, o.OptionsInventaire, sortie)
	if err != nil {
		return err
	}
	if len(plan.Manquantes) == 0 {
		return nil
	}
	if o.DryRun {
		_, _ = fmt.Fprintf(sortie, "\n--dry-run : rien n'a été fait.\n")
		return nil
	}
	if err := manquantesEnrolables(plan.Manquantes); err != nil {
		return err
	}
	if o.Confirmer != nil && !o.Confirmer(plan.Manquantes) {
		return errors.New("enrôlement abandonné")
	}

	// Un seul mot de passe pour tout le lot, sauf demande contraire.
	//
	// Le prix est dit plutôt que caché : le secret vit alors le temps du LOT et
	// non celui d'une requête. C'est la conséquence directe d'une seule saisie
	// pour N machines, et `--password-per-host` est le chemin qui ne la paie pas.
	var partage prompt.Secret
	if !o.ParMachine {
		partage, err = o.MotDePasse.Lire(ctx)
		if err != nil {
			return err
		}
		defer partage.Wipe()
	}

	resultats := make([]resultat, 0, len(plan.Manquantes))
	for i, m := range plan.Manquantes {
		_, _ = fmt.Fprintf(sortie, "\n--- %d/%d  %s ---\n", i+1, len(plan.Manquantes), m.Nom)

		emp, err := uneDuLot(ctx, api, suiveur, o, plan.Environnement, m, partage, sortie)
		resultats = append(resultats, resultat{Machine: m, Empreinte: emp, Err: err})
		if err != nil {
			_, _ = fmt.Fprintf(sortie, "\n%s : %v\n", m.Nom, err)
			if o.ArreterAuPremierEchec {
				break
			}
		}
	}

	return recapituler(sortie, resultats, len(plan.Manquantes))
}

// uneDuLot enrôle une machine, en lisant son mot de passe si le lot le demande.
func uneDuLot(ctx context.Context, api *client.Client, suiveur Suiveur, o OptionsLot,
	environnement string, m ansible.Machine, partage prompt.Secret, sortie io.Writer,
) (Empreinte, error) {
	motDePasse := partage
	apresEnvoi := func() {}
	if o.ParMachine {
		_, _ = fmt.Fprintf(sortie, "compte %s@%s\n", m.Login, m.Adresse)
		var err error
		motDePasse, err = o.MotDePasse.Lire(ctx)
		if err != nil {
			return Empreinte{}, err
		}
		// Lu pour cette machine, effacé dès que sa requête est partie : c'est ce
		// que `--password-per-host` achète.
		apresEnvoi = motDePasse.Wipe
		defer motDePasse.Wipe()
	}

	opts := OptionsEnrolement{
		Environment: environnement,
		Host:        m.Nom,
		Address:     m.Adresse,
		Port:        m.Port,
		Login:       m.Login,
		MotDePasse:  o.MotDePasse,
		Confirmer:   o.ConfirmerEmpreinte,
	}
	if err := opts.valider(); err != nil {
		return Empreinte{}, err
	}
	return enrolerUne(ctx, api, suiveur, opts, motDePasse, apresEnvoi, sortie)
}

// manquantesEnrolables refuse un lot AVANT la première connexion.
//
// Tout est vérifié d'un coup plutôt que machine par machine : découvrir à la
// septième qu'elle n'a pas de compte, après six enrôlements et six saisies,
// serait la pire façon de l'apprendre.
func manquantesEnrolables(machines []ansible.Machine) error {
	for _, m := range machines {
		if m.Login == "" {
			return fmt.Errorf("machine %s : aucun `ansible_user` dans l'inventaire — "+
				"warden ne sait pas quel compte ouvrir. Posez-le sur la machine ou "+
				"sur son groupe, ou restreignez le lot avec --group", m.Nom)
		}
	}
	return nil
}

// recapituler rend le compte de ce que le lot a fait.
//
// Un tableau à la fin, et pas seulement le fil des messages : sur dix machines,
// ce qu'on cherche est « lesquelles ont échoué », et le relire dans le
// défilement d'un terminal est exactement ce qu'on ne veut pas avoir à faire.
func recapituler(sortie io.Writer, resultats []resultat, vises int) error {
	_, _ = fmt.Fprintf(sortie, "\n\n=== récapitulatif ===\n\n")

	tw := tabwriter.NewWriter(sortie, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "MACHINE\tÉTAT\tEMPREINTE")
	var echecs int
	for _, r := range resultats {
		etat, empreinte := "enrôlée", r.Empreinte.Valeur
		if r.Err != nil {
			echecs++
			etat, empreinte = "ÉCHEC", r.Err.Error()
		}
		if empreinte == "" {
			empreinte = "-"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", r.Machine.Nom, etat, empreinte)
	}
	_ = tw.Flush()

	if ignorees := vises - len(resultats); ignorees > 0 {
		_, _ = fmt.Fprintf(sortie, "\n%d machine(s) non traitée(s) : le lot s'est "+
			"arrêté au premier échec.\n", ignorees)
	}

	_, _ = fmt.Fprintf(sortie, "\n%d enrôlée(s), %d en échec.\n", len(resultats)-echecs, echecs)
	if echecs > 0 {
		return fmt.Errorf("%d machine(s) n'ont pas été enrôlées", echecs)
	}
	return nil
}

// ConfirmerLotAuTerminal demande l'accord sur le plan, avant la première
// connexion.
//
// Une seule question pour tout le lot, et elle porte sur une liste que
// l'opérateur vient de lire : ce qui va être joint, sous quel compte, et
// lesquelles seront jointes par leur NOM plutôt que par une adresse. C'est le
// dernier moment où un inventaire mal filtré peut encore être arrêté.
func ConfirmerLotAuTerminal(entree io.Reader, sortie io.Writer) func([]ansible.Machine) bool {
	return func(machines []ansible.Machine) bool {
		_, _ = fmt.Fprintf(sortie, "\nwarden va se connecter à ces %d machine(s) et y poser "+
			"la clé de l'environnement.\nContinuer ? [o/N] ", len(machines))
		return lireOui(entree)
	}
}

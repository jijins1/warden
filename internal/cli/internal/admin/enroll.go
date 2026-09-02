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
	Confirmer func(Empreinte) bool
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

	// `motDePasse.Wipe` en `apresEnvoi` : le secret disparaît dès que la requête
	// est partie, et non à la fin de la fonction. Entre les deux il y a le RUN,
	// qui dure des minutes — c'est précisément l'intervalle pendant lequel il ne
	// doit plus être en mémoire.
	_, err = enrolerUne(ctx, api, suiveur, o, motDePasse, motDePasse.Wipe, sortie)
	return err
}

// enrolerUne est le corps de l'enrôlement, sur un secret DÉJÀ LU.
//
// La séparation existe pour le lot : `EnrolerLot` lit un mot de passe une fois
// et le réutilise pour N machines. Qui efface le secret, et QUAND, devient donc
// une question — d'où `apresEnvoi`, appelée dès que la requête est partie. Une
// machine seule y met `Wipe` ; un lot ne le peut pas, et l'efface à la fin.
func enrolerUne(ctx context.Context, api *client.Client, suiveur Suiveur, o OptionsEnrolement, motDePasse prompt.Secret, apresEnvoi func(), sortie io.Writer) (Empreinte, error) {
	var err error

	runRef := o.RunRef
	if runRef == "" {
		// Le MÊME générateur que `warden submit` : deux formes d'identifiant
		// dans le même produit finiraient par diverger, et c'est celui-ci que le
		// schéma contraint.
		runRef, err = upload.NewRunRef()
		if err != nil {
			return Empreinte{}, err
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
		return Empreinte{}, err
	}
	if apresEnvoi != nil {
		apresEnvoi()
	}

	_, _ = fmt.Fprintf(sortie, "enrôlement %s\n\n", runRef)

	res, fin, err := suivreLEnrolement(ctx, suiveur, runRef, sortie)
	if err != nil {
		return Empreinte{}, err
	}

	if fin.Status != "succeeded" {
		// L'empreinte est affichée MÊME en échec quand on l'a vue : un
		// enrôlement qui échoue après avoir constaté l'empreinte a quand même
		// appris quelque chose, et c'est ce qu'il faut déclarer pour épingler.
		if res.Fingerprint != "" {
			_, _ = fmt.Fprintf(sortie, "\nempreinte constatée : %s\n", res.Fingerprint)
		}
		if res.Substituee {
			// Le cas de l'intermédiaire, nommé comme tel. Un message générique
			// ferait RÉESSAYER, ce qui est précisément la mauvaise conduite : ce
			// qui répond à cette adresse n'est pas la machine attendue.
			_, _ = fmt.Fprintf(sortie,
				"\nLa machine qui répond à %s n'est PAS celle enrôlée sous le nom « %s ».\n"+
					"N'insistez pas : réessayer ne changera rien. Soit la machine a été\n"+
					"réinstallée — auquel cas retirez-la de l'inventaire puis réenrôlez-la —,\n"+
					"soit quelque chose d'autre répond à sa place.\n",
				o.Address, o.Host)
			return Empreinte{}, fmt.Errorf("empreinte de %s : la machine n'est pas celle attendue", o.Host)
		}
		return Empreinte{}, fmt.Errorf("l'enrôlement de %s a échoué (%s)", o.Host, fin.Status)
	}
	if res.HostKey == "" {
		// Le run dit avoir réussi mais n'a pas rapporté d'empreinte. Déclarer la
		// machine sans elle la laisserait non épinglée en la croyant enrôlée.
		return Empreinte{}, fmt.Errorf("l'enrôlement de %s s'est terminé sans rapporter d'empreinte", o.Host)
	}

	emp := Empreinte{Valeur: res.Fingerprint, Algorithme: algorithmeDe(res.HostKey)}
	_, _ = fmt.Fprintf(sortie, "\nempreinte de %s :\n  %s  (%s)\n", o.Host, emp.Valeur, emp.Algorithme)
	if o.Confirmer != nil && !o.Confirmer(emp) {
		_, _ = fmt.Fprintf(sortie, "\nDéclaration abandonnée. La clé EST posée sur %s ;\n"+
			"la machine n'est simplement pas enregistrée dans l'inventaire.\n", o.Host)
		return emp, errors.New("déclaration refusée")
	}

	// Le `run_ref` accompagne la déclaration : c'est lui qui permet au serveur
	// de constater que warden s'est bien connecté, donc d'enregistrer
	// `assisted`. Sans lui la machine serait notée `declared`, ce qui dirait
	// que le produit n'a jamais vu de mot de passe alors qu'il en a vu un.
	if _, err := api.DeclareEnrolledHost(ctx, o.Environment, runRef, client.Host{
		Name: o.Host, Environment: o.Environment, Address: o.Address,
		Port: port, Login: o.Login, HostKey: res.HostKey,
	}); err != nil {
		return emp, fmt.Errorf("la machine est enrôlée mais n'a pas pu être déclarée : %w", err)
	}

	_, _ = fmt.Fprintf(sortie, "\n%s est enrôlée et épinglée.\n", o.Host)
	return emp, nil
}

func (o OptionsEnrolement) valider() error {
	switch {
	case o.Environment == "":
		return errors.New("--env est requis")
	case o.Host == "":
		return errors.New("--host est requis")
	case o.Address == "":
		return errors.New("--address est requis : c'est là que l'enrôlement se " +
			"connecte. Une adresse IP, ou un nom que LE POD sait résoudre — sa vue " +
			"du DNS n'est pas la vôtre, et il ne lit pas votre ssh_config")
	case o.Login == "":
		return errors.New("--login est requis : quel compte warden doit-il ouvrir ?")
	case o.MotDePasse == nil:
		return errors.New("aucune provenance pour le mot de passe")
	}
	return nil
}

// Empreinte est ce qu'on soumet à l'utilisateur.
//
// Elle porte l'ALGORITHME et pas seulement la valeur, et ce n'est pas un détail
// d'affichage. Un sshd sert plusieurs clés d'hôte — ed25519, ECDSA, RSA — et
// c'est la NÉGOCIATION qui décide laquelle. Mesuré sur une vraie machine : la
// clé retenue était l'ECDSA alors que le message invitait à comparer avec le
// fichier ed25519, dont l'empreinte est évidemment différente.
//
// Une question de sécurité qui produit un écart quand tout va bien est pire
// qu'une absence de question : elle apprend à passer outre.
type Empreinte struct {
	Valeur     string // SHA256:…
	Algorithme string // ssh-ed25519, ecdsa-sha2-nistp256, ssh-rsa…
}

// Fichier rend le chemin du fichier de clé publique correspondant sur la
// machine, pour que la comparaison porte sur la BONNE clé.
func (e Empreinte) Fichier() string {
	switch {
	case strings.HasPrefix(e.Algorithme, "ssh-ed25519"):
		return "/etc/ssh/ssh_host_ed25519_key.pub"
	case strings.HasPrefix(e.Algorithme, "ecdsa-sha2-"):
		return "/etc/ssh/ssh_host_ecdsa_key.pub"
	case strings.HasPrefix(e.Algorithme, "ssh-rsa"), strings.HasPrefix(e.Algorithme, "rsa-sha2-"):
		return "/etc/ssh/ssh_host_rsa_key.pub"
	default:
		// Un algorithme qu'on ne connaît pas : plutôt que de désigner un fichier
		// au hasard, on invite à les lister tous. La comparaison reste possible.
		return "/etc/ssh/ssh_host_*_key.pub"
	}
}

// algorithmeDe extrait le type d'une ligne `authorized_keys`.
func algorithmeDe(ligne string) string {
	if i := strings.IndexByte(ligne, ' '); i > 0 {
		return ligne[:i]
	}
	return ligne
}

// resultatEnrolement est ce que le pod rapporte de la machine.
type resultatEnrolement struct {
	HostKey      string `json:"warden_host_key"`
	Fingerprint  string `json:"warden_fingerprint"`
	DejaPresente bool   `json:"warden_deja_presente"`

	// Substituee dit que la machine jointe n'est pas celle qui était épinglée.
	// Elle ne vient pas d'un payload mais de l'ÉTAT d'un event : le runner
	// émet `unreachable` pour ce cas précis, et `failed` pour tout le reste.
	Substituee bool `json:"-"`
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
			// Le drapeau de substitution est préservé : il vient d'un ÉTAT
			// d'event et non d'un payload, donc l'écrasement le perdrait.
			r.Substituee = res.Substituee
			res = r
		}

		// `unreachable` porte ici un sens précis : la machine jointe n'est pas
		// celle qu'on visait. Le runner le distingue de `failed` exprès, et
		// c'est cette distinction qui permet de dire à l'utilisateur d'enquêter
		// plutôt que de réessayer.
		if e.State == events.StateUnreachable {
			res.Substituee = true
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
func ConfirmerAuTerminal(entree io.Reader, sortie io.Writer) func(Empreinte) bool {
	return func(emp Empreinte) bool {
		// Le fichier nommé est celui de l'algorithme RÉELLEMENT négocié : en
		// désigner un autre ferait constater un écart légitime à l'utilisateur,
		// et lui apprendrait à passer outre.
		_, _ = fmt.Fprintf(sortie, "\nCette empreinte est-elle celle de la machine attendue ?\n"+
			"Sur la machine :  ssh-keygen -lf %s\n"+
			"Déclarer et épingler ? [o/N] ", emp.Fichier())

		return lireOui(entree)
	}
}

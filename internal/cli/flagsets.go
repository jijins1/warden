package cli

import (
	"flag"
	"io"
	"strings"

	"github.com/jijins1/warden/internal/cli/internal/command"
	"github.com/jijins1/warden/internal/cli/internal/prompt"
	"github.com/jijins1/warden/internal/cli/internal/submit"
)

// Les FlagSets sont construits ICI, et pas dans les commandes qui les utilisent.
//
// La raison est l'aide. Le troisième tiroir — `warden host declare --help` — rend
// les drapeaux depuis le `flag.FlagSet` RÉEL, ce qui est la seule façon de
// garantir qu'une page d'aide ne décrit pas un drapeau qui n'existe pas, ni ne
// tait un drapeau qui existe. Il faut donc pouvoir construire le FlagSet d'une
// commande sans l'exécuter.
//
// L'invariant de l'ancien découpage tient toujours : UN FlagSet par groupe,
// partagé par ses verbes. Ce qui a changé est que l'arbre d'aide nomme, verbe par
// verbe, LESQUELS de ces drapeaux le concernent — `warden host forget --help`
// montre --env et --host, pas -f. Un test vérifie la correspondance dans les deux
// sens, de sorte qu'un drapeau ajouté sans être documenté échoue à la
// compilation des tests plutôt qu'en silence.

// neuf construit un FlagSet muet : les messages d'erreur sont les nôtres.
func neuf(nom string) *flag.FlagSet {
	fs := flag.NewFlagSet(nom, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// parserEntremele parse `args` en acceptant les positionnels AU MILIEU des
// drapeaux, et rend ces positionnels dans l'ordre.
//
// `flag` s'arrête au premier argument qui n'est pas un drapeau : `warden run
// all -m ping` — la forme d'`ansible`, motif en tête — y laisserait `-m ping`
// non lu. On reprend donc le parse après chaque positionnel. `--` garde son
// sens : tout ce qui le suit est positionnel.
func parserEntremele(fs *flag.FlagSet, args []string) ([]string, error) {
	var positionnels []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		reste := fs.Args()
		if lus := len(args) - len(reste); lus > 0 && args[lus-1] == "--" {
			return append(positionnels, reste...), nil
		}
		if len(reste) == 0 {
			return positionnels, nil
		}
		positionnels = append(positionnels, reste[0])
		args = reste[1:]
	}
}

// verbosite porte les quatre drapeaux `-v`…`-vvvv` d'un FlagSet.
//
// QUATRE booléens et non un compteur, parce que `flag` ne sait pas répéter :
// `-v -v` ne compterait rien, il réaffecterait le même booléen. Les quatre noms
// sont donc déclarés séparément — ce sont d'ailleurs ceux qu'`ansible-playbook`
// accepte — et `niveau` les réduit à l'unique réglage qu'ils désignent.
type verbosite [submit.VerbositeMax]*bool

func declarerVerbosite(fs *flag.FlagSet) verbosite {
	aides := [submit.VerbositeMax]string{
		"détailler la sortie d'Ansible, comme ansible-playbook -v",
		"comme -v, en ajoutant le détail de chaque résultat de tâche",
		"comme -vv, en ajoutant les arguments des tâches et la commande de connexion",
		"comme -vvv, en ajoutant le debug de connexion ; très volumineux",
	}
	var v verbosite
	for i := range v {
		v[i] = fs.Bool(strings.Repeat("v", i+1), false, aides[i])
	}
	return v
}

// niveau rend le PLUS ÉLEVÉ des niveaux demandés.
//
// Le plus élevé, et non un refus comme côté serveur : `-v -vvv` tapé à la main
// est une hésitation, pas une contradiction, et ce qui part vers le serveur
// reste UN drapeau. Le refus a sa place là-bas, où deux niveaux simultanés ne
// peuvent venir que d'un client qui les a composés exprès.
func (v verbosite) niveau() int {
	n := 0
	for i, demande := range v {
		if *demande {
			n = i + 1
		}
	}
	return n
}

type drapeauxSubmit struct {
	repo, playbook, environnement, inventaire, runRef *string
	become                                            *prompt.Source

	// connexion est la saisie du mot de passe de connexion SSH (`-k`), et
	// compte le compte distant (`-u`) — ADR-0048.
	connexion *prompt.Source
	compte    *string

	// Les trois du §5.5 que l'ADR-0038 ouvre. Ils portent LE MÊME NOM que chez
	// `ansible-playbook`, et ce n'est pas de la coquetterie : la commande se
	// retrouve dans un historique de shell, et un `--dry-run` qui veut dire
	// `--check` obligerait à traduire dans les deux sens.
	check, diff *bool
	limit       *string
	extraVars   *submit.Repetable
	verbeux     verbosite

	// elever et eleverVers sont `--become` et `--become-user`, à ne pas confondre
	// avec `become` ci-dessus, qui est la SAISIE du mot de passe d'élévation.
	// Les deux se combinent : l'un demande l'élévation, l'autre en fournit le
	// secret.
	elever     *bool
	eleverVers *string

	// sansSuivi détache la commande du flux. Le NÉGATIF est le drapeau, parce
	// que le suivi est le défaut : un `--follow` obligerait à le taper à chaque
	// fois pour obtenir ce que fait `ansible-playbook` sans qu'on lui demande.
	sansSuivi *bool
}

func fsSubmit() (*flag.FlagSet, drapeauxSubmit) {
	fs := neuf("submit")
	f := drapeauxSubmit{
		repo:          fs.String("repo", "", "nom du dépôt côté serveur ; inutile si le fichier de projet déclare `repo:`"),
		playbook:      fs.String("playbook", "", "playbook à exécuter, relatif à la racine du dépôt"),
		environnement: fs.String("env", "", "environnement cible"),
		inventaire:    fs.String("inventory", "", "inventaire, relatif à la racine du dépôt"),
		// Rejouer une soumission interrompue produit le même commit d'audit, donc
		// le même SHA (ADR-0025) : la reprise est sûre, à condition de reprendre
		// le même identifiant.
		runRef: fs.String("run-ref", "", "reprendre une soumission interrompue"),

		check: fs.Bool("check", false, "simuler : Ansible annonce ce qu'il ferait sans le faire"),
		diff:  fs.Bool("diff", false, "montrer le contenu de ce qui change, tâche par tâche"),
		limit: fs.String("limit", "", "restreindre aux machines ou groupes de ce motif Ansible"),

		eleverVers: fs.String("become-user", "", "compte visé par l'élévation ; root par défaut"),

		sansSuivi: fs.Bool("no-follow", false,
			"rendre la main dès le run déclaré, sans afficher son déroulement"),
	}

	// `-b` et `--become` désignent le MÊME booléen, comme chez ansible-playbook.
	// Deux `fs.Bool` créeraient deux variables, et `-b` seul serait sans effet.
	f.elever = fs.Bool("become", false, "élever sur toutes les tâches ; se combine avec -K")
	fs.BoolVar(f.elever, "b", false, "abréviation d'--become, comme ansible-playbook")

	// Répétable, et déclaré sous ses DEUX noms comme `ansible-playbook` : `-e` est
	// celui qu'on tape, `--extra-vars` celui qu'on écrit dans un script.
	f.verbeux = declarerVerbosite(fs)

	f.extraVars = &submit.Repetable{}
	fs.Var(f.extraVars, "e", "poser une variable, sous la forme nom=valeur ; répétable")
	fs.Var(f.extraVars, "extra-vars", "identique à -e")
	// La MÊME mécanique de saisie que pour une clé ou un enrôlement : trois
	// drapeaux nommés pareil, jamais argv, jamais l'environnement.
	f.become = prompt.Declarer(fs, "become-pass", "le mot de passe d'élévation")
	// `-K` est le nom qu'`ansible-playbook` lui donne. Voir Abreger.
	f.become.Abreger(fs, "K", "abréviation d'--ask-become-pass, comme ansible-playbook")
	// `-k` et `-u` sont les noms d'`ansible-playbook` : le mot de passe de
	// connexion et le compte distant, pour les machines où chacun se connecte
	// en son nom (annuaire LDAP, ADR-0048).
	f.connexion = prompt.Declarer(fs, "pass", "le mot de passe de connexion")
	f.connexion.Abreger(fs, "k", "abréviation d'--ask-pass, comme ansible-playbook")
	f.compte = fs.String("user", "", "compte distant, pour les machines dont l'inventaire ne le nomme pas")
	fs.StringVar(f.compte, "u", "", "abréviation de --user, comme ansible-playbook")
	return fs, f
}

// drapeauxRun est le pendant ad-hoc de drapeauxSubmit : même dépôt,
// environnement, inventaire, élévation, extra-vars et suivi, mais un module et
// ses arguments à la place d'un playbook, et pas de `--limit` — c'est le motif
// de machines, PORTÉ EN POSITION comme le premier argument d'`ansible`, qui en
// tient lieu.
type drapeauxRun struct {
	repo, environnement, inventaire, runRef *string
	become                                  *prompt.Source

	connexion *prompt.Source
	compte    *string

	module, args *string

	check, diff *bool
	extraVars   *submit.Repetable
	verbeux     verbosite

	elever     *bool
	eleverVers *string

	sansSuivi *bool
}

func fsRun() (*flag.FlagSet, drapeauxRun) {
	fs := neuf("run")
	f := drapeauxRun{
		repo:          fs.String("repo", "", "nom du dépôt côté serveur ; inutile si le fichier de projet déclare `repo:`"),
		environnement: fs.String("env", "", "environnement cible"),
		inventaire:    fs.String("inventory", "", "inventaire, relatif à la racine du dépôt"),
		runRef:        fs.String("run-ref", "", "reprendre un run ad-hoc interrompu"),

		module: fs.String("module", "", "module Ansible à exécuter"),
		args:   fs.String("args", "", "arguments du module, comme -a chez `ansible`"),

		check: fs.Bool("check", false, "simuler : Ansible annonce ce qu'il ferait sans le faire"),
		diff:  fs.Bool("diff", false, "montrer le contenu de ce qui change"),

		eleverVers: fs.String("become-user", "", "compte visé par l'élévation ; root par défaut"),

		sansSuivi: fs.Bool("no-follow", false,
			"rendre la main dès le run déclaré, sans afficher son déroulement"),
	}
	fs.StringVar(f.module, "m", "", "abréviation de --module, comme `ansible`")
	fs.StringVar(f.args, "a", "", "abréviation de --args, comme `ansible`")

	f.elever = fs.Bool("become", false, "élever sur toutes les tâches ; se combine avec -K")
	fs.BoolVar(f.elever, "b", false, "abréviation d'--become, comme ansible-playbook")

	f.verbeux = declarerVerbosite(fs)

	f.extraVars = &submit.Repetable{}
	fs.Var(f.extraVars, "e", "poser une variable, sous la forme nom=valeur ; répétable")
	fs.Var(f.extraVars, "extra-vars", "identique à -e")
	f.become = prompt.Declarer(fs, "become-pass", "le mot de passe d'élévation")
	f.become.Abreger(fs, "K", "abréviation d'--ask-become-pass, comme ansible-playbook")
	// `-k` et `-u` sont les noms d'`ansible-playbook` : le mot de passe de
	// connexion et le compte distant, pour les machines où chacun se connecte
	// en son nom (annuaire LDAP, ADR-0048).
	f.connexion = prompt.Declarer(fs, "pass", "le mot de passe de connexion")
	f.connexion.Abreger(fs, "k", "abréviation d'--ask-pass, comme ansible-playbook")
	f.compte = fs.String("user", "", "compte distant, pour les machines dont l'inventaire ne le nomme pas")
	fs.StringVar(f.compte, "u", "", "abréviation de --user, comme ansible-playbook")
	return fs, f
}

type drapeauxEnroler struct {
	env, hote, adresse, login, runRef, groupe *string
	port                                      *int
	mdp                                       *prompt.Source
	toutes, dryRun, oui                       *bool
	parMachine, accepter, arreter             *bool
}

func fsEnroler() (*flag.FlagSet, drapeauxEnroler) {
	fs := neuf("enroll")
	f := drapeauxEnroler{
		env:     fs.String("env", "", "environnement"),
		hote:    fs.String("host", "", "nom de la machine, tel que l'inventaire Ansible la nomme"),
		adresse: fs.String("address", "", "où se connecter : une adresse, ou un nom que LE POD sait résoudre"),
		port:    fs.Int("port", 22, "port SSH"),
		login:   fs.String("login", "", "compte à ouvrir sur la machine"),
		runRef:  fs.String("run-ref", "", "reprendre un enrôlement interrompu"),

		toutes:     fs.Bool("all", false, "enrôler toutes les machines de l'inventaire qui ne le sont pas"),
		groupe:     fs.String("group", "", "restreindre --all à un groupe de l'inventaire"),
		dryRun:     fs.Bool("dry-run", false, "montrer le plan et s'arrêter là"),
		oui:        fs.Bool("yes", false, "ne pas demander confirmation du plan"),
		parMachine: fs.Bool("password-per-host", false, "un mot de passe par machine, au lieu d'un pour tout le lot"),
		accepter:   fs.Bool("accept-new-hostkeys", false, "ne PAS soumettre les empreintes ; elles restent au récapitulatif"),
		arreter:    fs.Bool("stop-on-error", false, "arrêter au premier échec au lieu de continuer"),
	}
	// Sans provenance explicite, la saisie au terminal est prise par défaut :
	// c'est le geste attendu de cette commande.
	f.mdp = prompt.Declarer(fs, "password", "le mot de passe du compte")
	return fs, f
}

type drapeauxHote struct {
	fichier, env, hote, groupe *string
	depuisInv, dryRun          *bool
}

func fsHote() (*flag.FlagSet, drapeauxHote) {
	fs := neuf("host")
	return fs, drapeauxHote{
		fichier:   fs.String("f", "", "fichier de déclaration (YAML ou JSON)"),
		env:       fs.String("env", "", "environnement"),
		hote:      fs.String("host", "", "machine"),
		depuisInv: fs.Bool("from-inventory", false, "lire l'inventaire ANSIBLE du dépôt courant"),
		groupe:    fs.String("group", "", "restreindre --from-inventory à un groupe"),
		dryRun:    fs.Bool("dry-run", false, "montrer le plan et s'arrêter là"),
	}
}

type drapeauxDepot struct {
	nom, remoteURL, mainline, remote *string
	oui                              *bool
}

func fsDepot() (*flag.FlagSet, drapeauxDepot) {
	fs := neuf("repo")
	return fs, drapeauxDepot{
		nom:       fs.String("name", "", "nom du dépôt côté serveur ; déduit de l'URL du remote"),
		remoteURL: fs.String("remote-url", "", "URL http(s) que le serveur clonera ; déduite du remote git"),
		mainline:  fs.String("mainline-ref", "", "branche de référence, en refs/heads/<nom>"),
		remote:    fs.String("remote", "", "remote git interrogé pour la déduction (défaut : origin)"),
		oui:       fs.Bool("yes", false, "ne pas demander confirmation"),
	}
}

type drapeauxCle struct {
	env *string
	cle *prompt.Source
}

func fsCle() (*flag.FlagSet, drapeauxCle) {
	fs := neuf("key")
	f := drapeauxCle{env: fs.String("env", "", "environnement")}
	// Multiligne : une clé PEM ne se saisit pas à l'aveugle, l'utilisateur ne
	// verrait rien de ce qu'il colle. Le registre le sait et refuse l'invite en
	// le disant.
	f.cle = prompt.DeclarerMultiligne(fs, "key", "la clé privée")
	return fs, f
}

type drapeauxVault struct {
	env, varName *string
	cle, valeur  *prompt.Source
}

func fsVault() (*flag.FlagSet, drapeauxVault) {
	fs := neuf("vault")
	f := drapeauxVault{
		env:     fs.String("env", "", "environnement"),
		varName: fs.String("var", "", "nom de la variable, dans le bloc !vault rendu"),
	}
	// PAS multiligne, contrairement à `key import` : un mot de passe de vault
	// est une valeur courte, comme celui d'un enrôlement — l'invite au
	// terminal est le chemin normal, pas un refus.
	f.cle = prompt.Declarer(fs, "vault", "le mot de passe de vault existant")
	f.valeur = prompt.Declarer(fs, "value", "la valeur à chiffrer")
	return fs, f
}

type drapeauxLogin struct {
	subject *string
	mdp     *prompt.Source
}

func fsLogin() (*flag.FlagSet, drapeauxLogin) {
	fs := neuf("login")
	f := drapeauxLogin{subject: fs.String("subject", "", "identifiant de votre compte")}
	// La MÊME mécanique de saisie que partout ailleurs : jamais argv, jamais
	// l'environnement, refus net quand deux provenances sont données.
	f.mdp = prompt.Declarer(fs, "password", "votre mot de passe")
	return fs, f
}

type drapeauxPasswd struct{ ancien, nouveau *prompt.Source }

func fsPasswd() (*flag.FlagSet, drapeauxPasswd) {
	fs := neuf("passwd")
	return fs, drapeauxPasswd{
		ancien:  prompt.Declarer(fs, "current-password", "votre mot de passe actuel"),
		nouveau: prompt.Declarer(fs, "new-password", "votre nouveau mot de passe"),
	}
}

type drapeauxUtilisateur struct {
	subject, nom *string
	admin        *bool
}

func fsUtilisateur() (*flag.FlagSet, drapeauxUtilisateur) {
	fs := neuf("user")
	return fs, drapeauxUtilisateur{
		subject: fs.String("subject", "", "identifiant du compte"),
		nom:     fs.String("name", "", "nom affiché dans l'audit"),
		admin:   fs.Bool("admin", false, "accorder l'administration"),
	}
}

type drapeauxDroit struct{ subject *string }

func fsDroit() (*flag.FlagSet, drapeauxDroit) {
	fs := neuf("grant")
	return fs, drapeauxDroit{subject: fs.String("subject", "", "identifiant du compte")}
}

// flagsPour rend le FlagSet d'un chemin de commande, pour l'affichage de l'aide.
//
// Une commande sans drapeau rend nil, et la page omet alors la section plutôt
// que d'afficher un titre vide.
func flagsPour(chemin []string) *flag.FlagSet {
	if len(chemin) == 0 {
		return nil
	}
	var fs *flag.FlagSet
	switch chemin[0] {
	case "submit":
		fs, _ = fsSubmit()
	case "run":
		fs, _ = fsRun()
	case "enroll":
		fs, _ = fsEnroler()
	case "host":
		fs, _ = fsHote()
	case "repo":
		fs, _ = fsDepot()
	case "key":
		fs, _ = fsCle()
	case "vault":
		fs, _ = fsVault()
	case "login":
		fs, _ = fsLogin()
	case "passwd":
		fs, _ = fsPasswd()
	case "user":
		fs, _ = fsUtilisateur()
	case "grant":
		fs, _ = fsDroit()
	case "logs":
		fs = fsLogs()
	}
	return fs
}

// fsLogs n'a AUCUN drapeau, et c'est délibéré : ce qu'on suit est un run, il se
// désigne par son identifiant, et un drapeau pour ça allongerait la commande
// qu'on tape le plus souvent après une soumission détachée.
func fsLogs() *flag.FlagSet { return neuf("logs") }

// aide écrit la page demandée sur la sortie standard.
//
// Sur STDOUT et non stderr, et l'appelant rend nil : une aide explicitement
// demandée est une commande qui a RÉUSSI. C'est ce qui la distingue d'une
// commande incomplète, dont l'ancienne implémentation la rapprochait en rendant
// l'aide sous forme d'erreur.
func aide(sortie io.Writer, args []string) error {
	command.Afficher(sortie, command.SansAide(args), flagsPour)
	return nil
}

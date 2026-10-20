package exec

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/jijins1/warden/internal/events"
)

// Binaire est le moteur, et il n'est pas réimplémenté (§11, décision #16).
const Binaire = "ansible-runner"

// maxLigne borne une ligne du flux d'events.
//
// Le `stdout` d'un module entre dans le payload et n'est borné par rien du côté
// d'Ansible. Sans cette borne, une seule tâche bavarde ferait tenir sa sortie
// entière en mémoire du pod. Au-delà, la ligne devient un event `unknown` qui
// dit qu'elle a été tronquée — jamais un arrêt du run.
//
// Sous le plafond d'un lot d'ingestion, sans quoi l'event serait produit puis
// refusé à l'envoi.
const maxLigne = 4 << 20 // 4 Mio

// DelaiApresSignalParDefaut laisse Ansible finir sa tâche courante après un
// SIGINT.
//
// Le §5.6 décrit l'escalade attendue : « Interruption demandée, attente de la
// fin de la tâche courante… ». Tuer immédiatement contredirait le message et
// laisserait une machine à moitié configurée sans que l'audit dise laquelle.
const DelaiApresSignalParDefaut = 30 * time.Second

// Config décrit ce qu'il faut exécuter.
type Config struct {
	// Workspace est l'arbre matérialisé du commit d'audit.
	Workspace string

	// PrivateDataDir est le répertoire de travail d'`ansible-runner`. Il DOIT
	// être inscriptible : la racine du pod ne l'est pas (§7).
	PrivateDataDir string

	// Playbook et Inventory sont des chemins RELATIFS au workspace, tels que
	// l'ordre de run les porte.
	Playbook  string
	Inventory string

	// Module, ModuleArgs et HostPattern décrivent un run AD-HOC — l'équivalent
	// de `ansible <motif> -m <module> -a <args>`, par opposition à
	// `ansible-playbook`. Mutuellement exclusifs avec Playbook : New() refuse
	// que les deux modes soient renseignés à la fois, ou ni l'un ni l'autre.
	//
	// Contrairement à Playbook et Inventory, ce ne sont PAS des chemins : ils
	// ne passent donc pas par `sousArbre`.
	Module      string
	ModuleArgs  string
	HostPattern string

	// Flags sont les flags passés à `ansible-playbook`, après citation.
	//
	// Ils SONT filtrés, depuis l'ADR-0038, et ce commentaire disait le contraire
	// jusque-là — à juste titre : il n'y avait alors aucune allowlist nulle part.
	// Le filtre vit côté serveur, dans `core.ValiderFlags`, et il refuse au lieu
	// d'amputer : une requête portant un drapeau non accepté est rejetée, donc ce
	// qui arrive ici est exactement ce qui a été demandé ET enregistré.
	//
	// Il est appliqué DEUX fois — à la création du run et à la relecture de
	// `run.flags` au moment de composer l'ordre — parce que la seconde est le
	// dernier point avant ce champ.
	//
	// Le filtre ne pourra JAMAIS vivre ici. Ce paquet tourne dans le pod runner,
	// qui est le domaine hostile de l'ADR-0011 : une allowlist appliquée par le
	// runner est une allowlist appliquée par le code qu'elle est censée
	// contraindre. Ce champ ne peut qu'en recevoir le résultat.
	Flags []string

	// SSHKey est la clé d'accès de l'environnement (ADR-0020), vide quand il
	// n'y en a pas.
	//
	// Elle est écrite dans `<pdd>/env/ssh_key`, et c'est MESURÉ :
	// `ansible-runner` y trouve la clé, la recopie, démarre un `ssh-agent`,
	// l'y charge par `ssh-add`, EFFACE le fichier, puis lance le playbook sous
	// cet agent. La clé n'existe donc sur disque qu'un instant, et le playbook
	// ne voit que l'agent.
	//
	// L'alternative — écrire un fichier et passer `--private-key` — la
	// laisserait lisible par le playbook pendant tout le run. Le playbook est
	// du code arbitraire (§2).
	SSHKey []byte

	// BecomePassword est le mot de passe d'élévation (§5.5), vide le plus
	// souvent.
	//
	// Il passe par `<pdd>/env/extravars`, et le chemin a été MESURÉ contre les
	// deux autres :
	//
	//   - `passwords` en kwargs fait basculer ansible-runner sous pexpect : le
	//     plugin d'audit n'est plus utilisé, les trames perdent leur champ
	//     `event`, `job_events` reste VIDE. L'audit disparaîtrait en silence —
	//     exactement ce que l'ADR-0021 redoute d'`ANSIBLE_STDOUT_CALLBACK` ;
	//   - `-e ansible_become_password=…` mettrait la valeur sur la ligne de
	//     commande, lisible par tout le monde sur la machine.
	//
	// Avec `extravars`, le flux reste entièrement structuré, la commande porte
	// `-e @<fichier>`, et la valeur n'apparaît nulle part dans le flux.
	BecomePassword []byte

	// ConnectionPassword est le mot de passe de connexion SSH de
	// l'utilisateur (ADR-0048), vide le plus souvent. Même chemin que
	// BecomePassword : `env/extravars`, sous `ansible_password`.
	ConnectionPassword []byte

	// HostKeyChecking impose une vérification STRICTE des empreintes contre
	// KnownHosts, seule source admise (ADR-0048). Un mot de passe de connexion
	// l'impose de toute façon : il ne part que vers une machine connue.
	HostKeyChecking bool

	// KnownHosts sont les lignes `known_hosts` des machines déclarées.
	KnownHosts []string

	// VaultPassword est la clé Ansible Vault de l'environnement (ADR-0045),
	// vide quand il n'y en a pas.
	//
	// Écrite dans `<pdd>/env/vault_password` (0600), puis livrée à
	// `ansible-playbook` par `--vault-password-file`, ajouté aux arguments
	// INTERNEMENT — jamais via Flags, le champ utilisateur filtré par
	// l'allowlist de l'ADR-0038. Ce n'est pas une régression de cette
	// allowlist : elle refuse un `--vault-password-file` FOURNI PAR
	// L'APPELANT, qui pointerait vers un fichier arbitraire du pod ; celui-ci
	// pointe vers un fichier que ce paquet vient d'écrire lui-même à partir
	// d'un secret émis par le serveur — exactement la même distinction que
	// pour SSHKey, qui ne passe pas non plus par Flags.
	//
	// Contrairement à SSHKey, le fichier n'est PAS effacé après lecture :
	// `ansible-playbook` le relit potentiellement à plusieurs reprises
	// pendant tout le run (chaque tâche qui référence un `!vault |` peut
	// déclencher une relecture), là où `ssh-agent` charge la clé SSH une fois
	// pour toute la durée du run. Il reste donc en clair sur le disque
	// éphémère du pod pendant la durée du run — risque déjà accepté pour
	// `extravars` (BecomePassword), pas un nouveau precedent.
	VaultPassword []byte

	// ForwardEnv porte les variables du shell de l'utilisateur retenues par la
	// composition serveur (§5.4).
	//
	// Elles sont posées dans `<pdd>/env/envvars`, qui est le mécanisme PROPRE
	// d'ansible-runner et non un fichier à nous. Ça a une conséquence qu'il faut
	// avoir en tête ici : ansible-runner lit ce fichier pour construire
	// l'environnement du processus `ansible-playbook`, et il PRÉFIXE certaines de
	// ses propres variables avec ce qu'il y trouve.
	//
	// MESURÉ sur ansible-runner 2.4.3 : `ANSIBLE_CALLBACK_PLUGINS` posée ici est
	// placée DEVANT le dossier de plugins d'ansible-runner, et comme Ansible
	// résout par nom, un `awx_display.py` de l'arbre audité est chargé à la place
	// du vrai. Le flux d'events se vide alors sans la moindre erreur — l'audit
	// disparaît en silence.
	//
	// Le filtre qui l'interdit vit CÔTÉ SERVEUR (`core.NomsReserves`) et ne peut
	// pas vivre ici : ce paquet tourne dans le domaine hostile de l'ADR-0011, et
	// une allowlist appliquée par le code qu'elle est censée contraindre ne
	// contraint rien.
	ForwardEnv map[string]string

	// DelaiApresSignal est le sursis accordé après le SIGINT du §5.6.
	// Zéro vaut DelaiApresSignalParDefaut.
	DelaiApresSignal time.Duration
}

// Runner pilote `ansible-runner` et rend son flux d'events.
type Runner struct {
	cfg Config
}

// New construit le pilote et échoue à froid.
func New(cfg Config) (*Runner, error) {
	if cfg.Workspace == "" {
		return nil, errors.New("exécution : espace de travail absent")
	}
	// Exactement un des deux modes : un playbook, ou un module ad-hoc contre un
	// motif de machines. Ni l'un ni l'autre laisserait `kwargs()` composer une
	// trame sans rien à exécuter ; les deux à la fois est une confusion que la
	// livraison de l'ordre a laissée passer, pas une intention.
	adhoc := cfg.Module != "" || cfg.HostPattern != ""
	switch {
	case cfg.Playbook == "" && !adhoc:
		return nil, errors.New("exécution : ni playbook ni module ad-hoc")
	case cfg.Playbook != "" && adhoc:
		return nil, errors.New("exécution : playbook et module ad-hoc à la fois")
	case adhoc && (cfg.Module == "" || cfg.HostPattern == ""):
		return nil, errors.New("exécution ad-hoc : module et motif de machines sont tous deux requis")
	}
	if cfg.PrivateDataDir == "" {
		return nil, errors.New("exécution : répertoire de travail absent")
	}
	// Le playbook et l'inventaire viennent de l'ordre de run, donc du serveur ;
	// ils n'en restent pas moins des chemins, et un `../` y ferait sortir de
	// l'arbre audité — c'est-à-dire exécuter autre chose que le commit
	// journalisé, ce que le §8.2 interdit. Un run ad-hoc n'a pas de playbook :
	// rien à confiner de ce côté.
	if cfg.Playbook != "" {
		if err := sousArbre(cfg.Playbook, "playbook"); err != nil {
			return nil, err
		}
	}
	if cfg.Inventory != "" {
		if err := sousArbre(cfg.Inventory, "inventaire"); err != nil {
			return nil, err
		}
	}
	if cfg.DelaiApresSignal <= 0 {
		cfg.DelaiApresSignal = DelaiApresSignalParDefaut
	}
	return &Runner{cfg: cfg}, nil
}

// Emetteur reçoit les events au fil de leur lecture.
//
// Une fonction et non une interface : l'appelant unique est le rapporteur, et
// `internal/runner/doc.go` note que l'agent est « une boucle autour
// d'ansible-runner et d'un canal », que l'envelopper de ports doublerait.
type Emetteur func(events.Event)

// Statut est l'issue rapportée par `ansible-runner` lui-même.
//
// Elle vient du flux et non du code de sortie du processus : le §6.4 rappelle
// que « l'exit code du pod dit que le processus est mort, pas ce que le
// playbook a fait ».
type Statut string

const (
	StatutReussi = Statut("successful")
	StatutEchoue = Statut("failed")
)

// Run exécute le playbook et pousse chaque event vers l'émetteur.
//
// L'erreur rendue signale une panne du PILOTAGE — binaire absent, flux
// illisible — jamais un playbook qui échoue. Un playbook en échec est un run
// `failed`, ce que le statut rend.
func (r *Runner) Run(ctx context.Context, emettre Emetteur) (Statut, error) {
	if err := os.MkdirAll(r.cfg.PrivateDataDir, 0o750); err != nil {
		return "", fmt.Errorf("préparation du répertoire de travail : %w", err)
	}
	if err := r.poserLaCle(); err != nil {
		return "", err
	}
	if err := r.poserLesVariables(); err != nil {
		return "", err
	}
	if err := r.poserLesExtravars(); err != nil {
		return "", err
	}
	if err := r.poserLaCleDeVault(); err != nil {
		return "", err
	}

	// Le PRÉ-CONTRÔLE, avant d'avoir lancé quoi que ce soit.
	//
	// Un playbook absent de l'arbre audité est l'échec le plus banal — une
	// faute de frappe — et c'est celui qu'`ansible-runner` explique le plus
	// mal : il rend `failed` sans qu'aucune tâche n'ait tourné, et le message
	// ne vit que dans une trame `verbose` que personne n'affichait. Le
	// constater ici donne un message ÉCRIT PAR NOUS, qui nomme le chemin
	// fautif, et évite de payer le démarrage d'un interpréteur pour rien.
	//
	// Ce n'est PAS une erreur au sens de cette fonction : le pilotage n'a pas
	// échoué, c'est le run qui n'a rien à exécuter. D'où `StatutEchoue` et un
	// event, comme pour n'importe quel run qui échoue.
	if manque := r.verifierLesChemins(); manque != "" {
		emettre(EventErreur(manque))
		return StatutEchoue, nil
	}

	kwargs, err := r.kwargs()
	if err != nil {
		return "", err
	}

	// #nosec G204 -- le binaire est une constante et le seul argument est le
	// répertoire de travail, qui vient de la configuration du pod et non de
	// l'ordre de run. Ce qui vient de l'ordre — playbook, inventaire, flags —
	// ne passe PAS par la ligne de commande : ces valeurs voyagent dans la
	// trame `kwargs` sur l'entrée standard, où elles sont du JSON et non des
	// arguments. C'est la raison de fond du choix de `worker`.
	cmd := exec.CommandContext(ctx, Binaire, "worker",
		"--private-data-dir="+r.cfg.PrivateDataDir)
	cmd.Stdin = strings.NewReader(kwargs)

	// SIGINT et non SIGKILL : c'est la sémantique native de Ctrl-C que le §5.6
	// exige de conserver, et la seule qui laisse Ansible terminer sa tâche
	// courante plutôt que d'abandonner une machine à moitié configurée.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGINT) }
	cmd.WaitDelay = r.cfg.DelaiApresSignal

	// La sortie d'erreur n'est PAS journalisée telle quelle : elle vient d'un
	// processus qui manipule les credentials du run. Elle est gardée bornée
	// pour le seul cas où le pilotage lui-même échoue.
	var stderr borne
	cmd.Stderr = &stderr

	// Un tube que NOUS possédons, et non `cmd.StdoutPipe()`. La raison est
	// l'annulation du §5.6 : le SIGINT va au processus, mais le tube reste
	// ouvert tant qu'un de ses ENFANTS le tient. Avec `StdoutPipe`, la lecture
	// se bloquerait là indéfiniment — donc avant d'atteindre `Wait`, donc sans
	// que `WaitDelay` puisse jamais s'appliquer. Le run annulé ne rendrait
	// jamais la main, et le pod mourrait sur `activeDeadlineSeconds` sans avoir
	// rapporté sa terminaison.
	lecture, ecriture, err := os.Pipe()
	if err != nil {
		return "", fmt.Errorf("flux d'events : %w", err)
	}
	cmd.Stdout = ecriture

	if err := cmd.Start(); err != nil {
		_ = lecture.Close()
		_ = ecriture.Close()
		return "", fmt.Errorf("démarrage d'%s : %w", Binaire, err)
	}
	// Le côté écriture appartient désormais au processus fils. Le garder ouvert
	// ici empêcherait la lecture de voir jamais la fin du flux.
	_ = ecriture.Close()

	fini := r.debloquer(ctx, lecture)
	b := r.lire(lecture, emettre)
	statut := b.statut

	// Le flux utile est terminé — sur la trame d'archive, le plus souvent. Le
	// RESTE doit quand même être consommé : l'archive d'artefacts suit, et un
	// tube plein bloquerait `ansible-runner` sur son écriture, donc `Wait` sur
	// un processus qui ne mourra jamais.
	_, _ = io.Copy(io.Discard, lecture)
	close(fini)
	_ = lecture.Close()

	if err := cmd.Wait(); err != nil && statut == "" {
		// Statut vide : le flux n'a rien dit de l'issue, donc l'échec du
		// processus est tout ce qu'on a.
		return "", fmt.Errorf("%s : %w (%s)", Binaire, err, stderr.String())
	}
	if statut == "" {
		return "", fmt.Errorf("%s s'est terminé sans rapporter de statut", Binaire)
	}

	// Le REPLI : un échec dont aucune tâche n'a tourné.
	//
	// Le run est `failed` et pas une seule machine n'a été touchée — erreur de
	// syntaxe, variable indéfinie, inventaire vide. Ansible l'a dit, mais dans
	// des trames `verbose` que rien ne montrait : elles deviennent des events
	// `unknown`, et l'interface les écarte à juste titre, puisqu'un run qui
	// RÉUSSIT en produit déjà (MESURÉ : cinq, pour un simple avertissement
	// d'interpréteur). D'où cette condition étroite — `failed` ET zéro tâche —
	// qui ne peut pas se déclencher sur un run ordinaire.
	//
	// `stderr` n'est PAS la source, et c'est contre-intuitif : MESURÉ, il est
	// VIDE dans ce cas. Le bornage de `borne` ne sert donc toujours qu'à la
	// panne de pilotage traitée juste au-dessus.
	if statut == StatutEchoue && b.executes == 0 && len(b.verbeux) > 0 {
		emettre(EventErreur(strings.Join(b.verbeux, " ")))
	}
	return statut, nil
}

// verifierLesChemins rend le message d'un chemin manquant, vide si tout est là.
//
// Les chemins sont RELATIFS au workspace, et `New` a déjà refusé qu'ils en
// sortent : il ne reste qu'à constater leur présence. Un run ad-hoc n'a pas de
// playbook — `Module` remplace le fichier — donc il n'y a rien à vérifier de
// ce côté.
//
// Le message NOMME le chemin demandé : c'est la seule chose que l'utilisateur
// puisse corriger, et la comparer à ce qu'il a tapé est tout le diagnostic.
func (r *Runner) verifierLesChemins() string {
	chemins := []struct{ quoi, rel string }{
		{"playbook", r.cfg.Playbook},
		{"inventaire", r.cfg.Inventory},
	}
	for _, c := range chemins {
		if c.rel == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(r.cfg.Workspace, c.rel)); err != nil {
			return fmt.Sprintf("%s introuvable dans le commit audité : %s", c.quoi, c.rel)
		}
	}
	return ""
}

// debloquer arme la sortie de secours de la lecture.
//
// Passé le sursis du §5.6, le tube est forcé en échéance : la lecture rend la
// main même si un enfant d'`ansible-runner` le tient encore ouvert. C'est la
// troisième marche de l'escalade — « Arrêt forcé en cours… » — vue du pilote.
func (r *Runner) debloquer(ctx context.Context, lecture *os.File) chan struct{} {
	fini := make(chan struct{})
	go func() {
		select {
		case <-fini:
			return
		case <-ctx.Done():
		}
		select {
		case <-fini:
		case <-time.After(r.cfg.DelaiApresSignal):
			_ = lecture.SetReadDeadline(time.Now())
		}
	}()
	return fini
}

// bilan retient, en plus du statut, de quoi EXPLIQUER un échec sans tâche.
//
// Les deux champs ajoutés ne servent qu'à ce cas : un run qui exécute quelque
// chose se raconte par ses events, et le diagnostic n'a alors rien à dire.
type bilan struct {
	statut Statut

	// executes compte les events porteurs d'un HÔTE, c'est-à-dire le travail
	// réel. Le reste — cycle de vie du play, `verbose` — n'est pas une tâche
	// qui a tourné sur une machine, et c'est la distinction qui décide si un
	// diagnostic est nécessaire.
	executes int

	// verbeux garde les textes des trames `verbose`, dans l'ordre. Ansible y
	// écrit ses erreurs fatales, découpées en fragments.
	verbeux []string
}

// lire consomme le flux JSONL et rend le dernier statut annoncé.
func (r *Runner) lire(sortie io.Reader, emettre Emetteur) bilan {
	tr := NewTraducteur()
	sc := bufio.NewScanner(sortie)
	sc.Buffer(make([]byte, 0, 64*1024), maxLigne)

	var b bilan
	for sc.Scan() {
		ligne := sc.Bytes()

		// MESURÉ : le flux se termine par `{"zipfile": N}` suivi de N octets
		// binaires. Lire au-delà transformerait une archive en events
		// `unknown`, c'est-à-dire du bruit d'audit que rien ne purge.
		if FinDeFlux(ligne) {
			break
		}
		if s := Status(ligne); s != "" {
			b.statut = Statut(s)
			continue
		}
		// Relu AVANT la traduction, et sans la remplacer : la trame `verbose`
		// part dans le flux d'events comme avant.
		if txt := TexteVerbeux(ligne); txt != "" {
			b.verbeux = append(b.verbeux, txt)
		}
		if e, ok := tr.Traduire(ligne); ok {
			if e.Host != "" {
				b.executes++
			}
			emettre(e)
		}
	}
	if err := sc.Err(); err != nil {
		// Une ligne trop longue, ou une lecture coupée. L'event qui le dit vaut
		// mieux qu'un silence : le traducteur ne fait pas échouer un run.
		slog.Warn("flux d'events tronqué", slog.String("error", err.Error()))
		emettre(events.Event{
			OccurredAt: time.Now().UTC(),
			State:      events.StateUnknown,
			Payload:    json.RawMessage(`{"warden_flux":"lecture du flux d'events interrompue"}`),
		})
	}
	return b
}

// poserLaCle écrit la clé d'accès là où `ansible-runner` la cherche.
//
// 0600, et le répertoire 0700. `ssh-add` ne s'en formalise pas — c'est `ssh -i`
// qui refuse une clé lisible par tous — mais le fichier existe le temps que le
// playbook démarre, et le playbook tourne sous le même utilisateur.
//
// Rien n'est écrit quand il n'y a pas de clé : un `env/ssh_key` vide ferait
// démarrer `ssh-agent` et échouer `ssh-add`, donc échouer le run entier — alors
// qu'un run sans clé doit simplement tourner sans clé.
func (r *Runner) poserLaCle() error {
	if len(r.cfg.SSHKey) == 0 {
		return nil
	}

	env := filepath.Join(r.cfg.PrivateDataDir, "env")
	if err := os.MkdirAll(env, 0o700); err != nil {
		return fmt.Errorf("préparation du répertoire de clés : %w", err)
	}
	// Le message ne cite jamais le contenu, et le chemin suffit à diagnostiquer.
	if err := os.WriteFile(filepath.Join(env, "ssh_key"), r.cfg.SSHKey, 0o600); err != nil {
		return fmt.Errorf("écriture de la clé d'accès : %w", err)
	}
	return nil
}

// cheminCleDeVault est le chemin du fichier de mot de passe de vault, dans le
// même répertoire `env/` qu'`ansible-runner` connaît déjà pour la clé SSH et
// `extravars` — pas un chemin de plus à faire connaître aux deux moitiés du
// runner.
func (r *Runner) cheminCleDeVault() string {
	return filepath.Join(r.cfg.PrivateDataDir, "env", "vault_password")
}

// poserLaCleDeVault écrit la clé de vault là où `--vault-password-file` ira
// la lire (ADR-0045).
//
// Contrairement à `poserLaCle`, le fichier n'est PAS effacé après lecture par
// un mécanisme équivalent à `ssh-agent` : `ansible-playbook` le relit à sa
// convenance pendant tout le run, chaque tâche qui référence un `!vault |`
// pouvant déclencher une relecture. Il reste donc en clair sur le disque
// éphémère du pod pour la durée du run — voir le commentaire de
// `Config.VaultPassword`.
func (r *Runner) poserLaCleDeVault() error {
	if len(r.cfg.VaultPassword) == 0 {
		return nil
	}

	env := filepath.Join(r.cfg.PrivateDataDir, "env")
	if err := os.MkdirAll(env, 0o700); err != nil {
		return fmt.Errorf("préparation du répertoire de clés : %w", err)
	}
	if err := os.WriteFile(r.cheminCleDeVault(), r.cfg.VaultPassword, 0o600); err != nil {
		return fmt.Errorf("écriture de la clé de vault : %w", err)
	}
	return nil
}

// poserLesExtravars écrit les mots de passe d'élévation et de connexion, et la
// vérification des empreintes, là où Ansible les lira.
//
// Le fichier est écrit ICI et non laissé à `ansible-runner` : celui-ci pose bien
// 0600 — c'est mesuré — mais s'en remettre à un comportement qu'on n'a pas écrit
// pour la protection d'un mot de passe, c'est le perdre le jour où il change.
//
// Rien n'est écrit quand il n'y en a pas : un `extravars` vide ferait passer un
// `-e @fichier` inutile à chaque run.
func (r *Runner) poserLesExtravars() error {
	vars := map[string]string{}
	if len(r.cfg.BecomePassword) > 0 {
		vars["ansible_become_password"] = string(r.cfg.BecomePassword)
	}
	if len(r.cfg.ConnectionPassword) > 0 {
		vars["ansible_password"] = string(r.cfg.ConnectionPassword)
	}
	if r.cfg.HostKeyChecking || len(r.cfg.ConnectionPassword) > 0 {
		chemin, err := r.poserLesEmpreintes()
		if err != nil {
			return err
		}
		// La vérification des empreintes, en extravars et non en variables
		// d'environnement : MESURÉ, un inventaire portant
		// `ansible_host_key_checking: false` ou `ansible_ssh_common_args:
		// -o StrictHostKeyChecking=no` l'emportait sur l'environnement. Les
		// extravars ont la priorité la plus haute, et `ssh_args` vient en tête
		// de la ligne de ssh, où la première valeur d'une option gagne
		// (ADR-0048). Les trois premières options sont le défaut d'Ansible.
		vars["ansible_host_key_checking"] = "true"
		vars["ansible_ssh_args"] = "-C -o ControlMaster=auto -o ControlPersist=60s" +
			" -o UserKnownHostsFile=" + chemin +
			" -o GlobalKnownHostsFile=/dev/null -o StrictHostKeyChecking=yes"
	}
	if len(vars) == 0 {
		return nil
	}

	env := filepath.Join(r.cfg.PrivateDataDir, "env")
	if err := os.MkdirAll(env, 0o700); err != nil {
		return fmt.Errorf("préparation du répertoire d'environnement : %w", err)
	}

	// `encoding/json` échappe ce qu'il faut : un mot de passe contenant un
	// guillemet ou un saut de ligne produirait sinon un fichier illisible, et
	// l'erreur parlerait de YAML plutôt que du mot de passe.
	brut, err := json.Marshal(vars)
	if err != nil {
		// Le message ne cite jamais la valeur.
		return errors.New("mots de passe : encodage impossible")
	}
	if err := os.WriteFile(filepath.Join(env, "extravars"), brut, 0o600); err != nil {
		return fmt.Errorf("écriture des mots de passe : %w", err)
	}
	return nil
}

// poserLesEmpreintes écrit le `known_hosts` du run et rend son chemin. Il est écrit même vide : un fichier vide refuse toutes les
// machines, ce qui est exactement ce qu'on veut quand aucune n'est déclarée.
func (r *Runner) poserLesEmpreintes() (string, error) {
	chemin := filepath.Join(r.cfg.PrivateDataDir, "known_hosts")
	var contenu strings.Builder
	for _, ligne := range r.cfg.KnownHosts {
		// Une ligne par entrée, et rien qui puisse en ajouter une autre.
		if strings.ContainsAny(ligne, "\r\n") {
			return "", errors.New("empreintes : une entrée contient un saut de ligne")
		}
		contenu.WriteString(ligne)
		contenu.WriteByte('\n')
	}
	if err := os.WriteFile(chemin, []byte(contenu.String()), 0o600); err != nil {
		return "", fmt.Errorf("écriture des empreintes : %w", err)
	}
	return chemin, nil
}

// poserLesVariables écrit les variables transmises là où ansible-runner les lit.
//
// `env/envvars` attend un mapping JSON, et `encoding/json` échappe ce qu'il faut :
// une valeur portant un guillemet ou un saut de ligne produirait sinon un fichier
// que le moteur relit de travers, avec une erreur parlant de YAML plutôt que de
// la variable en cause.
//
// 0600 posé par NOUS et non laissé à ansible-runner, pour la même raison que le
// mot de passe d'élévation : s'en remettre à un comportement qu'on n'a pas écrit
// pour la protection d'un secret, c'est le perdre le jour où il change.
//
// Rien n'est écrit quand il n'y a rien : un `envvars` vide ferait travailler
// ansible-runner sur un fichier sans contenu à chaque run.
func (r *Runner) poserLesVariables() error {
	if len(r.cfg.ForwardEnv) == 0 {
		return nil
	}

	env := filepath.Join(r.cfg.PrivateDataDir, "env")
	if err := os.MkdirAll(env, 0o700); err != nil {
		return fmt.Errorf("préparation du répertoire d'environnement : %w", err)
	}

	brut, err := json.Marshal(r.cfg.ForwardEnv)
	if err != nil {
		// Le message ne cite jamais les valeurs.
		return errors.New("variables transmises : encodage impossible")
	}
	if err := os.WriteFile(filepath.Join(env, "envvars"), brut, 0o600); err != nil {
		return fmt.Errorf("écriture des variables transmises : %w", err)
	}
	return nil
}

// kwargs compose la trame que `ansible-runner worker` lit sur son entrée.
//
// MESURÉ : le `worker` accepte une trame `kwargs` seule et travaille alors sur
// un `--private-data-dir` DÉJÀ peuplé. La variante nominale — `transmit` en
// amont — ferait transiter par un tube une archive zip de tout l'arbre audité,
// pour un résultat identique.
func (r *Runner) kwargs() (string, error) {
	args := map[string]any{
		// L'arbre matérialisé sert directement de répertoire de projet : le
		// recopier sous le `private_data_dir` doublerait la taille du volume
		// pour rien.
		"project_dir": r.cfg.Workspace,
		// Ansible ne colore que sur un terminal, et le worker n'en a pas. Le
		// texte rendu de chaque event — le `stdout` de premier niveau, que la
		// CLI réaffiche tel quel — arriverait sinon sans couleur. C'est la
		// couleur d'ANSIBLE, pas une imitation : la CLI n'a qu'à la retirer
		// quand sa propre sortie n'est pas un terminal.
		//
		// Ici et non dans `env/envvars` : MESURÉ sur ansible-runner 2.4.3, les
		// deux se FUSIONNENT. Le forçage ne dépend donc pas des variables
		// transmises, et ne réécrit pas leur fichier.
		"envvars": map[string]string{"ANSIBLE_FORCE_COLOR": "1"},
	}
	if r.cfg.Playbook != "" {
		args["playbook"] = r.cfg.Playbook
	} else {
		// Mode ad-hoc : `ansible-runner worker` accepte `module`/`module_args`/
		// `host_pattern` à la place de `playbook`, exactement comme la commande
		// `ansible` accepte `-m`/`-a`/un motif là où `ansible-playbook` attend un
		// fichier. New() garantit que ce cas n'est atteint qu'avec Module et
		// HostPattern tous deux renseignés.
		args["module"] = r.cfg.Module
		args["host_pattern"] = r.cfg.HostPattern
		if r.cfg.ModuleArgs != "" {
			args["module_args"] = r.cfg.ModuleArgs
		}
	}
	if r.cfg.Inventory != "" {
		args["inventory"] = filepath.Join(r.cfg.Workspace, r.cfg.Inventory)
	}
	// `--vault-password-file` est ajouté ICI, jamais via `r.cfg.Flags` : voir le
	// commentaire de `Config.VaultPassword`. Il précède les flags de
	// l'utilisateur dans la liste, sans que l'ordre ait d'incidence pour
	// `ansible-playbook`.
	cmdline := make([]string, 0, len(r.cfg.Flags)+2)
	if len(r.cfg.VaultPassword) > 0 {
		cmdline = append(cmdline, "--vault-password-file", r.cheminCleDeVault())
	}
	cmdline = append(cmdline, r.cfg.Flags...)
	if len(cmdline) > 0 {
		args["cmdline"] = citerTous(cmdline)
	}

	brut, err := json.Marshal(map[string]any{"kwargs": args})
	if err != nil {
		return "", fmt.Errorf("composition des paramètres d'%s : %w", Binaire, err)
	}
	// `eof` clôt l'entrée : sans elle le worker attend une archive qui ne
	// viendra pas.
	return string(brut) + "\n{\"eof\":true}\n", nil
}

// citerTous compose la ligne de commande passée à `ansible-playbook`.
//
// `ansible-runner` découpe `cmdline` à la façon d'un shell. Les flags viennent
// pourtant du serveur sous forme de LISTE, filtrée par l'allowlist du §5.5
// (ADR-0038) : les recoller avec des espaces rendrait à la valeur d'un
// `--limit` le pouvoir d'introduire d'autres drapeaux, ce qui contournerait
// l'allowlist par le seul champ qu'elle laisse libre. La citation restitue le
// découpage voulu.
func citerTous(flags []string) string {
	cites := make([]string, 0, len(flags))
	for _, f := range flags {
		cites = append(cites, citer(f))
	}
	return strings.Join(cites, " ")
}

// citer met un argument entre apostrophes à la manière de shlex.quote.
func citer(s string) string {
	if s == "" {
		return "''"
	}
	sur := true
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-', c == '_', c == '.', c == '/', c == '=', c == ',', c == ':', c == '+':
		default:
			sur = false
		}
		if !sur {
			break
		}
	}
	if sur {
		return s
	}
	// Une apostrophe ferme la citation, en insère une littérale, et la rouvre.
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// sousArbre refuse un chemin qui sortirait de l'arbre audité.
func sousArbre(chemin, quoi string) error {
	if filepath.IsAbs(chemin) {
		return fmt.Errorf("exécution : %s doit être un chemin relatif à l'arbre audité", quoi)
	}
	propre := filepath.Clean(chemin)
	if propre == ".." || strings.HasPrefix(propre, ".."+string(filepath.Separator)) {
		return fmt.Errorf("exécution : %s sort de l'arbre audité", quoi)
	}
	return nil
}

// borne accumule au plus quelques kilo-octets.
//
// La sortie d'erreur d'`ansible-runner` n'est pas bornée, et elle vient d'un
// processus qui a vu les credentials du run. On en garde de quoi diagnostiquer
// une panne de pilotage, pas de quoi remplir un log.
type borne struct {
	b []byte
}

const maxStderr = 4096

func (b *borne) Write(p []byte) (int, error) {
	if reste := maxStderr - len(b.b); reste > 0 {
		b.b = append(b.b, p[:min(reste, len(p))]...)
	}
	return len(p), nil
}

func (b *borne) String() string { return strings.TrimSpace(string(b.b)) }

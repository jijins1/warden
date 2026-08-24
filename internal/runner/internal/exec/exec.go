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

	// Flags sont les flags passés à `ansible-playbook`, après citation.
	//
	// Ils ne sont PAS filtrés. La politique serveur du §5.5 n'existe pas : ce
	// sont les flags que le client a DEMANDÉS, recopiés tels quels depuis le
	// corps de la requête (`api/run_handler.go`). C'est l'écart que l'ADR-0021
	// assume, et que `core.NewRun.Flags` énonce déjà correctement — demandé et
	// effectif coïncident parce qu'il n'y a rien entre les deux.
	//
	// Le filtre ne pourra JAMAIS vivre ici. Ce paquet tourne dans le pod runner,
	// qui est le domaine hostile de l'ADR-0011 : une allowlist appliquée par le
	// runner est une allowlist appliquée par le code qu'elle est censée
	// contraindre. Elle doit vivre côté serveur et par environnement (§5.5) ; ce
	// champ ne peut qu'en recevoir le résultat.
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
	if cfg.Playbook == "" {
		return nil, errors.New("exécution : playbook absent")
	}
	if cfg.PrivateDataDir == "" {
		return nil, errors.New("exécution : répertoire de travail absent")
	}
	// Le playbook et l'inventaire viennent de l'ordre de run, donc du serveur ;
	// ils n'en restent pas moins des chemins, et un `../` y ferait sortir de
	// l'arbre audité — c'est-à-dire exécuter autre chose que le commit
	// journalisé, ce que le §8.2 interdit.
	if err := sousArbre(cfg.Playbook, "playbook"); err != nil {
		return nil, err
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
	if err := r.poserLeBecome(); err != nil {
		return "", err
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
	statut := r.lire(lecture, emettre)

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
	return statut, nil
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

// lire consomme le flux JSONL et rend le dernier statut annoncé.
func (r *Runner) lire(sortie io.Reader, emettre Emetteur) Statut {
	tr := NewTraducteur()
	sc := bufio.NewScanner(sortie)
	sc.Buffer(make([]byte, 0, 64*1024), maxLigne)

	var statut Statut
	for sc.Scan() {
		ligne := sc.Bytes()

		// MESURÉ : le flux se termine par `{"zipfile": N}` suivi de N octets
		// binaires. Lire au-delà transformerait une archive en events
		// `unknown`, c'est-à-dire du bruit d'audit que rien ne purge.
		if FinDeFlux(ligne) {
			break
		}
		if s := Status(ligne); s != "" {
			statut = Statut(s)
			continue
		}
		if e, ok := tr.Traduire(ligne); ok {
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
	return statut
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

// poserLeBecome écrit le mot de passe d'élévation là où Ansible le lira.
//
// Le fichier est écrit ICI et non laissé à `ansible-runner` : celui-ci pose bien
// 0600 — c'est mesuré — mais s'en remettre à un comportement qu'on n'a pas écrit
// pour la protection d'un mot de passe, c'est le perdre le jour où il change.
//
// Rien n'est écrit quand il n'y en a pas : un `extravars` vide ferait passer un
// `-e @fichier` inutile à chaque run.
func (r *Runner) poserLeBecome() error {
	if len(r.cfg.BecomePassword) == 0 {
		return nil
	}

	env := filepath.Join(r.cfg.PrivateDataDir, "env")
	if err := os.MkdirAll(env, 0o700); err != nil {
		return fmt.Errorf("préparation du répertoire d'environnement : %w", err)
	}

	// `encoding/json` échappe ce qu'il faut : un mot de passe contenant un
	// guillemet ou un saut de ligne produirait sinon un fichier illisible, et
	// l'erreur parlerait de YAML plutôt que du mot de passe.
	brut, err := json.Marshal(map[string]string{
		"ansible_become_password": string(r.cfg.BecomePassword),
	})
	if err != nil {
		// Le message ne cite jamais la valeur.
		return errors.New("mot de passe d'élévation : encodage impossible")
	}
	if err := os.WriteFile(filepath.Join(env, "extravars"), brut, 0o600); err != nil {
		return fmt.Errorf("écriture du mot de passe d'élévation : %w", err)
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
		"playbook": r.cfg.Playbook,
		// L'arbre matérialisé sert directement de répertoire de projet : le
		// recopier sous le `private_data_dir` doublerait la taille du volume
		// pour rien.
		"project_dir": r.cfg.Workspace,
	}
	if r.cfg.Inventory != "" {
		args["inventory"] = filepath.Join(r.cfg.Workspace, r.cfg.Inventory)
	}
	if len(r.cfg.Flags) > 0 {
		args["cmdline"] = citerTous(r.cfg.Flags)
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
// pourtant du serveur sous forme de LISTE, déjà filtrée par l'allowlist du
// §5.5 : les recoller avec des espaces rendrait à la valeur d'un
// `--extra-vars` le pouvoir d'en introduire d'autres. La citation restitue le
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

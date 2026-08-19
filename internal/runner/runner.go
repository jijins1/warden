// Package runner est le point d'entrée de l'agent qui s'exécute dans le pod.
//
// Domaine de confiance : hostile par construction (§2 — le playbook lancé ici
// est du code arbitraire). Ce binaire est lisible par un adversaire, ce qui est
// la raison d'être de sa séparation : son image ne doit contenir ni le code du
// control plane, ni ses dépendances (ADR-0011).
package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jijins1/warden/internal/git"
	"github.com/jijins1/warden/internal/runner/internal/dispatch"
	"github.com/jijins1/warden/internal/runner/internal/exec"
	"github.com/jijins1/warden/internal/runner/internal/fetch"
	"github.com/jijins1/warden/internal/runner/internal/report"
	wire "github.com/jijins1/warden/internal/wire/dispatch"
	ingestwire "github.com/jijins1/warden/internal/wire/ingest"
)

// Les NOMS des variables viennent du contrat de fil et ne sont pas redéclarés
// ici : l'orchestrateur les pose, ce paquet les lit, et deux déclarations
// jumelles finiraient par diverger — ce qui s'est produit, `WARDEN_DISPATCH_URL`
// n'étant posé par personne alors que ce paquet l'exigeait.
const (
	DefaultWorkspace = "/workspace"

	// MountDir est le répertoire où l'orchestrateur monte le Secret du jeton. Il
	// borne ce que lireJeton accepte de lire.
	MountDir = "/var/run/warden"

	// delaiDeCloture borne l'écriture de la terminaison du run.
	//
	// Il court sur un contexte détaché, donc rien d'autre ne l'arrêtera : sans
	// borne, un pod dont l'ingestion ne répond plus resterait en vie jusqu'à
	// `activeDeadlineSeconds` au lieu de rendre la main.
	delaiDeCloture = 2 * time.Minute
)

// Run exécute l'agent jusqu'à l'annulation du contexte.
func Run(ctx context.Context, args []string) error {
	if len(args) > 0 {
		// Aucun argument n'est accepté, et ce n'est pas de la rigueur gratuite :
		// la ligne de commande d'un pod est visible et modifiable par qui édite
		// la podspec. Tout ce que le runner doit savoir vient du canal de
		// dispatch, dont l'authenticité est établie par son jeton.
		return fmt.Errorf("warden-runner ne prend aucun argument (%d reçus)", len(args))
	}

	token, err := lireJeton(os.Getenv(wire.EnvTokenFile))
	if err != nil {
		return err
	}

	canal, err := dispatch.New(os.Getenv(wire.EnvDispatchURL), token)
	if err != nil {
		return err
	}

	slog.Info("attente de l'ordre")
	mission, err := canal.Await(ctx)
	if err != nil {
		if errors.Is(err, dispatch.ErrCanceled) {
			// Annulation avant l'ordre : le run a été interrompu pendant le
			// démarrage du pod. Ce n'est pas un échec de l'agent.
			slog.Info("annulation reçue avant l'ordre")
			return nil
		}
		return err
	}

	// À partir d'ici le pod détient des credentials. Rien de ce qui suit ne les
	// journalise : le type Secret de l'ADR-0018 les masque à l'impression, et le
	// respecter ici est ce qui rend cette garantie utile.
	switch {
	case mission.Enroll != nil:
		return enroler(ctx, *mission.Enroll, token)
	case mission.Order != nil:
		return jouerLeRun(ctx, *mission.Order, token)
	default:
		// Ni l'un ni l'autre : le client a rendu une mission vide, ce qu'il ne
		// devrait jamais faire. Le dire plutôt que déréférencer.
		return errors.New("mission reçue sans contenu")
	}
}

// jouerLeRun matérialise le code puis exécute le playbook (§5.2).
func jouerLeRun(ctx context.Context, ordre wire.Order, token string) error {
	slog.Info("ordre reçu", slog.String("run_ref", ordre.RunRef),
		slog.String("audit_sha", ordre.AuditSHA))

	commit, err := git.NewSHA(ordre.AuditSHA)
	if err != nil {
		return fmt.Errorf("ordre du run %s : commit invalide", ordre.RunRef)
	}

	espace := os.Getenv(wire.EnvWorkspace)
	if espace == "" {
		espace = DefaultWorkspace
	}

	if err := fetch.Materialize(ctx, espace, ordre.GitURL, string(ordre.GitToken), commit); err != nil {
		return fmt.Errorf("run %s : %w", ordre.RunRef, err)
	}

	slog.Info("code matérialisé", slog.String("run_ref", ordre.RunRef),
		slog.String("workspace", espace))

	return executer(ctx, ordre, espace, token)
}

// executer joue le playbook et rapporte ce qu'il fait.
//
// L'ORDRE porte la correction. Le rapporteur est ouvert AVANT l'exécution et
// fermé APRÈS, quoi qu'il arrive : le §6.3 pose qu'un run interrompu brutalement
// doit encore dire ce qui avait déjà été modifié, ce qui suppose que le canal de
// remontée existe avant la première tâche et survive à l'échec de la dernière.
func executer(ctx context.Context, ordre wire.Order, espace, token string) error {
	// Aucun rédacteur : l'ordre ne porte pas encore de `forward_env`, donc aucune
	// variable de playbook n'est injectée dans ce pod. La couture de l'ADR-0017
	// est en place dans `report` et s'appliquera sans toucher à ce chemin le jour
	// où elles arriveront.
	//
	// La clé d'accès, elle, N'ENTRE PAS dans le jeu à masquer, et c'est la
	// distinction que l'ADR-0020 pose : les credentials d'accès viennent du
	// serveur et n'apparaissent jamais dans un résultat de module ni dans un
	// `--diff`. Ce que l'ADR-0017 masque est l'autre famille — les valeurs que
	// le playbook rend dans des fichiers.
	rapporteur, err := report.New(os.Getenv(ingestwire.EnvIngestURL), token, nil)
	if err != nil {
		return fmt.Errorf("run %s : remontée des events : %w", ordre.RunRef, err)
	}

	moteur, err := exec.New(exec.Config{
		Workspace: espace,
		// Frère de l'arbre audité, sous le même volume inscriptible : la racine
		// du pod est en lecture seule (§7), et `ansible-runner` a besoin
		// d'écrire ses artefacts.
		PrivateDataDir: filepath.Join(filepath.Dir(espace), "pdd"),
		Playbook:       ordre.Playbook,
		Inventory:      ordre.Inventory,
		Flags:          ordre.Flags,
		// `Reveal` et non un passage implicite : le nom inconfortable doit se
		// voir ici, c'est le seul endroit du runner où la clé d'accès sort de
		// son enveloppe masquée.
		SSHKey: ordre.SSHKey.Reveal(),
		// Le mot de passe d'élévation vient de l'utilisateur, pas du serveur :
		// il ne relève pas du magasin de l'ADR-0020 et ne fait que transiter.
		BecomePassword: ordre.BecomePassword.Reveal(),
	})
	if err != nil {
		// Le run est clos malgré tout : sans cela il resterait `running` pour
		// toujours, et l'audit ne dirait pas pourquoi rien n'a eu lieu.
		return joindre(clore(ctx, rapporteur, ingestwire.StatusFailed, ""),
			fmt.Errorf("run %s : %w", ordre.RunRef, err))
	}

	slog.Info("exécution du playbook", slog.String("run_ref", ordre.RunRef),
		slog.String("playbook", ordre.Playbook), slog.String("inventaire", ordre.Inventory))

	statut, err := moteur.Run(ctx, rapporteur.Send)

	// Ce que le RAPPORTEUR a constaté prime : si l'audit ne s'écrit plus, le run
	// ne peut pas être déclaré réussi, quoi qu'Ansible en dise (§6.3).
	if errReport := rapporteur.Err(); errReport != nil {
		return joindre(clore(ctx, rapporteur, ingestwire.StatusFailed, ""),
			fmt.Errorf("run %s : %w", ordre.RunRef, errReport))
	}
	if err != nil {
		// Panne de PILOTAGE, distincte d'un playbook qui échoue. Si le contexte
		// est annulé, c'est le Ctrl-C du §5.6 et non un défaut.
		final, cause := ingestwire.StatusFailed, ingestwire.Cause("")
		if ctx.Err() != nil {
			final, cause = ingestwire.StatusInterrupted, ingestwire.CauseUserInterrupt
		}
		return joindre(clore(ctx, rapporteur, final, cause),
			fmt.Errorf("run %s : %w", ordre.RunRef, err))
	}

	final := ingestwire.StatusFailed
	if statut == exec.StatutReussi {
		final = ingestwire.StatusSucceeded
	}
	slog.Info("run terminé", slog.String("run_ref", ordre.RunRef),
		slog.String("statut", string(final)))

	// Un playbook en ÉCHEC ne fait pas sortir l'agent en erreur, et c'est
	// délibéré : le §6.4 pose que « l'exit code du pod dit que le processus est
	// mort, pas ce que le playbook a fait ». Le verdict est dans le journal, où
	// il est requêtable ; le faire aussi porter par le code de sortie créerait
	// une seconde source de vérité, qui divergerait le jour où la clôture
	// échoue sans que le playbook ait échoué.
	return clore(ctx, rapporteur, final, "")
}

// clore vide le dernier lot et clôt le run.
//
// Le contexte est DÉTACHÉ de celui du run : l'annulation du §5.6 est justement
// le moment où il reste le plus à dire, et fermer avec un contexte déjà annulé
// ferait échouer l'écriture de la terminaison qu'on essaie de journaliser.
func clore(ctx context.Context, r *report.Reporter, statut ingestwire.Status, cause ingestwire.Cause) error {
	fond, annuler := context.WithTimeout(context.WithoutCancel(ctx), delaiDeCloture)
	defer annuler()

	return r.Close(fond, ingestwire.Final{
		Status: statut, Cause: cause, SecretsUnmasked: r.SecretsUnmasked(),
	})
}

// joindre rend l'erreur de clôture ET celle du run, sans qu'aucune n'efface
// l'autre : « le playbook a échoué » et « l'audit n'a pas pu s'écrire » sont
// deux incidents différents, et confondre les deux égare le diagnostic.
func joindre(cloture, run error) error {
	if cloture == nil {
		return run
	}
	return errors.Join(run, fmt.Errorf("clôture du run : %w", cloture))
}

// lireJeton lit le jeton d'amorçage depuis son fichier monté.
//
// Un fichier et non une variable d'environnement, pour la raison donnée
// plus haut. Sa lecture échoue bruyamment : un runner sans jeton ne peut rien
// faire, et le laisser continuer produirait un échec plus loin, avec un message
// parlant d'autre chose.
func lireJeton(chemin string) (string, error) {
	if chemin == "" {
		return "", fmt.Errorf("%s n'est pas définie", wire.EnvTokenFile)
	}
	// Le chemin est contraint au répertoire de montage, et ce n'est pas de la
	// complaisance envers l'analyseur. Ce binaire tourne dans le même conteneur
	// que le playbook : tout ce qui vient de son environnement est, à terme,
	// influençable par du code arbitraire. Lire un chemin arbitraire ferait de
	// cette fonction une primitive de lecture de fichier au service de qui
	// contrôle l'environnement.
	propre := filepath.Clean(chemin)
	if !strings.HasPrefix(propre, MountDir+string(filepath.Separator)) {
		return "", fmt.Errorf("le jeton d'amorçage doit être monté sous %s", MountDir)
	}

	brut, err := os.ReadFile(propre) // #nosec G304 G703 -- chemin contraint à MountDir juste au-dessus
	if err != nil {
		return "", fmt.Errorf("lecture du jeton d'amorçage : %w", err)
	}

	jeton := strings.TrimSpace(string(brut))
	if jeton == "" {
		return "", errors.New("le jeton d'amorçage monté est vide")
	}
	return jeton, nil
}

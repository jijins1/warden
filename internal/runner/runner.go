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
	"github.com/jijins1/warden/internal/runner/internal/redact"
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
	// LE RÉDACTEUR est armé des valeurs INJECTÉES dans ce run (ADR-0017).
	//
	// La clé d'accès N'ENTRE PAS dans le jeu à masquer, et c'est la distinction
	// que l'ADR-0020 pose : les credentials d'accès viennent du serveur et
	// n'apparaissent jamais dans un résultat de module ni dans un `--diff`. Ce
	// que l'ADR-0017 masque est l'autre famille — les valeurs que le playbook
	// rend dans des fichiers, c'est-à-dire précisément celles qu'on lui injecte.
	rapporteur, err := report.New(os.Getenv(ingestwire.EnvIngestURL), token,
		redacteurPour(ordre))
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
		// Les variables du shell, déjà filtrées par la composition serveur
		// (§5.4.1). Le filtre ne peut pas vivre ici : ce binaire tourne dans le
		// domaine hostile de l'ADR-0011.
		ForwardEnv: enClair(ordre.ForwardEnv),
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
	// Le pod meurt SOUS le run. MESURÉ sur le cluster : à l'expiration
	// d'`activeDeadlineSeconds`, le signal annule le contexte, `ansible-runner`
	// est interrompu et rapporte « failed » — que le playbook ait échoué ou non.
	// Le clore ici inscrirait « votre playbook a échoué » à propos d'un playbook
	// qui n'a rien dit, c'est-à-dire un mensonge dans le journal d'audit.
	//
	// Le runner rapporte donc ce qu'il SAIT — ses events, ce qui avait déjà été
	// modifié (§6.3) — et laisse la clôture au réconciliateur, seul à voir
	// « DeadlineExceeded », « Evicted » ou « OOMKilled » (§6.4, marche 3). Ce
	// renvoi n'est sûr que parce que ce composant existe : avant lui, le run
	// serait resté en vol pour toujours.
	//
	// Un succès CONSTATÉ fait exception : si `ansible-runner` a rapporté
	// « successful », le playbook est allé au bout et le signal est arrivé après.
	// C'est un fait observé, pas une supposition, et le taire perdrait l'issue
	// d'un run qui a réussi.
	if ctx.Err() != nil && !(err == nil && statut == exec.StatutReussi) {
		slog.Warn("run abandonné au réconciliateur",
			slog.String("run_ref", ordre.RunRef),
			slog.String("motif", "le pod s'arrête ; l'issue observée ne serait pas celle du playbook"))
		return joindre(abandonner(ctx, rapporteur),
			fmt.Errorf("run %s : arrêt du pod : %w", ordre.RunRef, ctx.Err()))
	}

	if err != nil {
		// Panne de PILOTAGE, distincte d'un playbook qui échoue.
		return joindre(clore(ctx, rapporteur, ingestwire.StatusFailed, ""),
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
// abandonner vide le dernier lot SANS clore, et pour la même raison que `clore`
// détache son contexte : c'est au moment de l'arrêt qu'il reste le plus à dire.
func abandonner(ctx context.Context, r *report.Reporter) error {
	fond, annuler := context.WithTimeout(context.WithoutCancel(ctx), delaiDeCloture)
	defer annuler()

	return r.Abandonner(fond)
}

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

// Bornes de la rédaction (ADR-0017).
//
// L'ADR pose que le seuil est « un paramètre de politique serveur, pas une
// constante du code ». La politique serveur est écartée en v1 (ADR-0021), donc
// il est ici — et il devra déménager avec elle plutôt que d'être recopié.
const (
	redactionLongueurMin = 5
	redactionSeuil       = 100
)

// redacteurPour arme le rédacteur des valeurs injectées dans ce run.
//
// Rend nil quand il n'y a rien à masquer, ce que `report` traite déjà : un
// rédacteur sans aiguille ferait parcourir chaque ligne du flux pour rien.
//
// Le mot de passe d'élévation EST du jeu. Il est injecté par `env/extravars`,
// donc il relève exactement de la famille que l'ADR-0017 vise — les valeurs
// qu'on donne au playbook et qu'il peut recopier dans un fichier. Que le flux
// mesuré ne le contienne pas aujourd'hui ne dit rien de ce qu'un playbook en
// fera.
func redacteurPour(ordre wire.Order) *redact.Redactor {
	secrets := make([]redact.Secret, 0, len(ordre.ForwardEnv)+1)
	for nom, v := range ordre.ForwardEnv {
		secrets = append(secrets, redact.Secret{Name: nom, Value: string(v.Reveal())})
	}
	if len(ordre.BecomePassword) > 0 {
		secrets = append(secrets, redact.Secret{
			Name: "ansible_become_password", Value: string(ordre.BecomePassword.Reveal()),
		})
	}
	if len(secrets) == 0 {
		return nil
	}
	return redact.NewRedactor(secrets, redactionLongueurMin, redactionSeuil)
}

// enClair sort les valeurs de leur enveloppe masquée.
//
// Une fonction nommée plutôt qu'une boucle en ligne : c'est le seul endroit du
// runner où les variables transmises perdent leur protection d'impression, et ça
// doit se voir en revue — comme le `Reveal` de la clé d'accès juste au-dessus.
func enClair(m map[string]wire.Secret) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for nom, v := range m {
		out[nom] = string(v.Reveal())
	}
	return out
}

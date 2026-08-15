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

	"github.com/jijins1/warden/internal/git"
	"github.com/jijins1/warden/internal/runner/internal/dispatch"
	"github.com/jijins1/warden/internal/runner/internal/fetch"
)

// Variables d'environnement posées par l'orchestrateur.
//
// Le CHEMIN du jeton voyage par l'environnement, jamais le jeton lui-même :
// l'objet `Pod` est celui que le produit liste, observe et journalise le plus, et
// un secret dans le champ le plus consulté du système est un secret qui finira
// dans un log (ADR-0023). Un chemin n'est pas un secret.
const (
	// #nosec G101 -- c'est le NOM d'une variable d'environnement portant un
	// chemin, pas un credential. Le jeton lui-même n'apparaît nulle part dans ce
	// binaire, et c'est précisément l'objet de cette indirection.
	EnvTokenFile     = "WARDEN_TOKEN_FILE"
	EnvDispatchURL   = "WARDEN_DISPATCH_URL"
	EnvWorkspace     = "WARDEN_WORKSPACE"
	DefaultWorkspace = "/workspace"

	// MountDir est le répertoire où l'orchestrateur monte le Secret du jeton. Il
	// borne ce que lireJeton accepte de lire.
	MountDir = "/var/run/warden"
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

	token, err := lireJeton(os.Getenv(EnvTokenFile))
	if err != nil {
		return err
	}

	canal, err := dispatch.New(os.Getenv(EnvDispatchURL), token)
	if err != nil {
		return err
	}

	slog.Info("attente de l'ordre de run")
	ordre, err := canal.Await(ctx)
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
	slog.Info("ordre reçu", slog.String("run_ref", ordre.RunRef),
		slog.String("audit_sha", ordre.AuditSHA))

	commit, err := git.NewSHA(ordre.AuditSHA)
	if err != nil {
		return fmt.Errorf("ordre du run %s : commit invalide", ordre.RunRef)
	}

	espace := os.Getenv(EnvWorkspace)
	if espace == "" {
		espace = DefaultWorkspace
	}

	if err := fetch.Materialize(ctx, espace, ordre.GitURL, string(ordre.GitToken), commit); err != nil {
		return fmt.Errorf("run %s : %w", ordre.RunRef, err)
	}

	slog.Info("code matérialisé", slog.String("run_ref", ordre.RunRef),
		slog.String("workspace", espace))

	// L'EXÉCUTION N'EST PAS ENCORE ICI. Le §5.2 la fait suivre, mais elle suppose
	// l'émission d'events vers l'ingestion — dont le contrat de fil
	// (`internal/wire/ingest`) n'existe encore d'aucun côté. Exécuter sans
	// pouvoir rapporter produirait un run qui tourne et dont l'audit ne dit rien,
	// ce qui est exactement l'inverse du produit.
	return errors.New("exécution du playbook : pas encore implémentée")
}

// lireJeton lit le jeton d'amorçage depuis son fichier monté.
//
// Un fichier et non une variable d'environnement, pour la raison donnée
// plus haut. Sa lecture échoue bruyamment : un runner sans jeton ne peut rien
// faire, et le laisser continuer produirait un échec plus loin, avec un message
// parlant d'autre chose.
func lireJeton(chemin string) (string, error) {
	if chemin == "" {
		return "", fmt.Errorf("%s n'est pas définie", EnvTokenFile)
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

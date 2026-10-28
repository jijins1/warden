package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/jijins1/warden/internal/cli/internal/client"
	"github.com/jijins1/warden/internal/cli/internal/miseajour"
	"github.com/jijins1/warden/internal/version"
)

// EnvMiseAJour remplace l'hôte des releases par un miroir (ADR-0049).
const EnvMiseAJour = "WARDEN_UPDATE_URL"

// envRelance marque le processus relancé après une mise à jour.
//
// C'est le garde-fou contre une boucle : un binaire neuf qui annoncerait encore
// une autre version que le serveur — une release compilée sans sa version —
// redemanderait la mise à jour, se relancerait, et ainsi de suite.
const envRelance = "WARDEN_RELANCE_APRES_MISE_A_JOUR"

// delaiTelechargement couvre le binaire entier, pas une seule requête d'API.
const delaiTelechargement = 5 * time.Minute

// aligneeSurLeServeur bloque une soumission tant que la CLI n'est pas à la
// version du serveur, et propose la mise à jour (ADR-0049).
//
// Sans terminal, personne ne peut répondre : la commande échoue en nommant
// `warden upgrade`, plutôt que de deviner dans un sens ou dans l'autre.
//
// En cas d'accord, la fonction ne REVIENT PAS : le processus est remplacé par
// le binaire neuf, relancé avec les mêmes arguments.
func aligneeSurLeServeur(ctx context.Context, api *client.Client, entree *os.File, sortie io.Writer) error {
	serveur, err := api.VersionServeur(ctx)
	if err != nil {
		return err
	}
	locale := version.Version
	if !ecart(locale, serveur) {
		return nil
	}
	if os.Getenv(envRelance) != "" {
		return fmt.Errorf("la CLI vient d'être mise à jour mais annonce v%s, pas v%s : "+
			"la release est incohérente — réinstallez la CLI à la main", locale, serveur)
	}
	if !term.IsTerminal(int(entree.Fd())) { //nolint:gosec // G115 : un descripteur tient dans un int.
		return fmt.Errorf("la CLI (v%s) ne correspond pas au serveur (v%s) : "+
			"lancez « warden upgrade »", locale, serveur)
	}

	_, _ = fmt.Fprintf(sortie, "La CLI (v%s) ne correspond pas au serveur (v%s).\n"+
		"Mettre à jour maintenant ? [O/n] ", locale, serveur)
	if !accepte(entree) {
		return fmt.Errorf("commande annulée : la CLI (v%s) ne correspond pas au serveur (v%s)",
			locale, serveur)
	}

	exe, err := installer(ctx, serveur)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(sortie, "CLI mise à jour en v%s.\n\n", serveur)

	if err := os.Setenv(envRelance, "1"); err != nil {
		return err
	}
	return miseajour.Relancer(exe)
}

// ecart dit si la CLI doit être ramenée à la version du serveur.
//
// Un build de développement, d'un côté ou de l'autre, ne déclenche rien : c'est
// le poste d'un développeur, qui sait ce qu'il a compilé. L'écart est
// symétrique : une CLI plus RÉCENTE que le serveur est ramenée elle aussi.
func ecart(locale, serveur string) bool {
	return !version.EstDev(locale) && !version.EstDev(serveur) && locale != serveur
}

// accepte lit un oui. La réponse vide — Entrée — est un OUI : la question n'est
// posée que pour laisser une porte de sortie, la mise à jour est ce qu'on
// attend. Une entrée fermée sans réponse (Ctrl-D) est un NON.
//
// Lue directement et non par `prompt.ReadLine` : ce n'est pas un secret, et
// celle-ci refuse justement la ligne vide qui est ici la réponse attendue.
func accepte(entree io.Reader) bool {
	ligne, err := bufio.NewReader(io.LimitReader(entree, 256)).ReadString('\n')
	if err != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(ligne)) {
	case "", "o", "oui", "y", "yes":
		return true
	default:
		return false
	}
}

// installer met en place la release `v` et rend le chemin du binaire neuf.
func installer(ctx context.Context, v string) (string, error) {
	exe, err := miseajour.CheminDuBinaire()
	if err != nil {
		return "", err
	}
	err = miseajour.Installer(ctx, miseajour.Options{
		Version:     v,
		BaseURL:     os.Getenv(EnvMiseAJour),
		ClePublique: miseajour.ClePublique,
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
		Executable:  exe,
		HTTP:        &http.Client{Timeout: delaiTelechargement},
	})
	return exe, err
}

// commandeUpgrade met la CLI à la version du serveur, à la demande.
//
// Sans session : la version est anonyme côté serveur, et une session expirée
// — ou une CLI trop ancienne pour en ouvrir une — ne doit pas empêcher de se
// mettre à jour.
func commandeUpgrade(ctx context.Context, args []string, sortie io.Writer) error {
	if len(args) > 0 {
		return errors.New("warden upgrade ne prend pas d'argument")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("répertoire courant illisible : %w", err)
	}
	adresse, err := adresseServeurDans(cwd)
	if err != nil {
		return err
	}
	api, err := client.New(client.Config{BaseURL: adresse})
	if err != nil {
		return err
	}
	serveur, err := api.VersionServeur(ctx)
	if err != nil {
		return err
	}

	locale := version.Version
	switch {
	case version.EstDev(serveur):
		_, _ = fmt.Fprintf(sortie, "Le serveur %s n'annonce pas de version publiée : rien à installer.\n", adresse)
		return nil
	case locale == serveur:
		_, _ = fmt.Fprintf(sortie, "La CLI est à jour (v%s).\n", locale)
		return nil
	}

	// Une CLI de développement est remplacée elle aussi : ici, la mise à jour
	// est DEMANDÉE, ce qui n'est pas le cas du contrôle fait par `submit`.
	if _, err := installer(ctx, serveur); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(sortie, "CLI mise à jour : %s → v%s.\n", affichee(locale), serveur)
	return nil
}

func affichee(v string) string {
	if version.EstDev(v) {
		return version.Dev
	}
	return "v" + v
}

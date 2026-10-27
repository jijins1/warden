package cli

import (
	"context"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/jijins1/warden/internal/cli/internal/client"
	"github.com/jijins1/warden/internal/cli/internal/command"
	"github.com/jijins1/warden/internal/cli/internal/projet"
	"github.com/jijins1/warden/internal/cli/internal/session"
)

//go:embed scripts/completion.bash
var scriptBash []byte

//go:embed scripts/completion.zsh
var scriptZsh []byte

// commandeCompletion écrit le script d'auto-complétion du shell demandé.
//
// Le script rappelle `warden __complete` à chaque Tab : rien n'est figé au
// moment de la génération, donc le script d'hier reste valide après un
// `warden` qui a gagné une commande.
func commandeCompletion(args []string, sortie io.Writer) error {
	if len(args) != 1 {
		//nolint:staticcheck // ST1005 : message terminal, jamais enveloppé.
		return errors.New("« warden completion » attend un shell : bash ou zsh.\n" +
			"« warden help completion » le détaille.")
	}

	var script []byte
	switch args[0] {
	case "bash":
		script = scriptBash
	case "zsh":
		script = scriptZsh
	default:
		return fmt.Errorf("shell inconnu : %q — « warden completion » ne sait générer que bash ou zsh", args[0])
	}

	_, err := sortie.Write(script)
	return err
}

// commandeInterneComplete est le backend de la complétion : `warden
// __complete` n'est jamais tapée par un humain, seulement par les scripts que
// `warden completion` a émis.
//
// Elle ne rend JAMAIS d'erreur : une frappe de Tab ne doit jamais planter un
// shell, seulement ne rien proposer.
func commandeInterneComplete(ctx context.Context, args []string, sortie io.Writer) error {
	if len(args) == 0 {
		args = []string{""}
	}
	cur := args[len(args)-1]
	avant := args[:len(args)-1]

	noeud, _, fs := command.Arbre().Localiser(avant, flagsPour)

	var candidats []command.Candidat
	switch {
	case len(avant) > 0 && drapeauAvecValeur(fs, avant[len(avant)-1]):
		for _, v := range valeursDynamiques(ctx, strings.TrimLeft(avant[len(avant)-1], "-"), avant) {
			candidats = append(candidats, command.Candidat{Mot: v})
		}
	case len(noeud.Sous) > 0:
		// Un GROUPE n'accepte jamais de drapeau avant qu'un verbe soit choisi
		// (`commandeHote` et ses pareilles ne parsent les drapeaux qu'À PARTIR
		// du second argument) : seuls des verbes ont un sens ici.
		candidats = noeud.CandidatsVerbes()
	default:
		// Une FEUILLE n'a que des drapeaux. Un positionnel comme le
		// <run-ref> de `warden logs` n'a pas de candidat statique — et pas
		// de source serveur non plus, faute d'un endpoint qui liste les runs.
		candidats = noeud.CandidatsDrapeaux(fs)
	}

	// Un candidat par ligne, sa description après une tabulation : c'est le
	// script du shell qui décide de l'afficher (zsh) ou de la mettre en forme
	// (bash), pas ce backend.
	for _, c := range candidats {
		if !strings.HasPrefix(c.Mot, cur) {
			continue
		}
		if c.Description == "" {
			_, _ = fmt.Fprintln(sortie, c.Mot)
		} else {
			_, _ = fmt.Fprintf(sortie, "%s\t%s\n", c.Mot, c.Description)
		}
	}
	return nil
}

// drapeauAvecValeur dit si `mot` nomme, dans `fs`, un drapeau qui attend une
// valeur — auquel cas ce n'est pas au mot SUIVANT de nommer un verbe ou un
// autre drapeau, mais à cette valeur d'être proposée.
func drapeauAvecValeur(fs *flag.FlagSet, mot string) bool {
	if fs == nil || !strings.HasPrefix(mot, "-") {
		return false
	}
	f := fs.Lookup(strings.TrimLeft(mot, "-"))
	return f != nil && !booleen(f)
}

func booleen(f *flag.Flag) bool {
	_, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok
}

// sourcesDynamiques associe un nom de drapeau à la façon de lister ce qu'il
// accepte. Un drapeau absent de cette table (--playbook, --group,
// --become-user...) n'a simplement AUCUN candidat dynamique — ce n'est pas
// une erreur, juste l'absence d'une liste à proposer.
var sourcesDynamiques = map[string]func(ctx context.Context, api *client.Client, avant []string) []string{
	"env": func(ctx context.Context, api *client.Client, _ []string) []string {
		cles, err := api.ListKeys(ctx)
		if err != nil {
			return nil
		}
		out := make([]string, len(cles))
		for i, k := range cles {
			out[i] = k.Environment
		}
		return out
	},
	"repo": func(ctx context.Context, api *client.Client, _ []string) []string {
		depots, err := api.ListRepos(ctx)
		if err != nil {
			return nil
		}
		out := make([]string, len(depots))
		for i, d := range depots {
			out[i] = d.Name
		}
		return out
	},
	"subject": func(ctx context.Context, api *client.Client, _ []string) []string {
		comptes, err := api.ListerComptes(ctx)
		if err != nil {
			return nil
		}
		out := make([]string, len(comptes))
		for i, c := range comptes {
			out[i] = c.Subject
		}
		return out
	},
	"host": func(ctx context.Context, api *client.Client, avant []string) []string {
		hotes, err := api.ListHosts(ctx, valeurDrapeau(avant, "env"))
		if err != nil {
			return nil
		}
		out := make([]string, len(hotes))
		for i, h := range hotes {
			out[i] = h.Name
		}
		return out
	},
}

// valeurDrapeau cherche, dans les mots déjà tapés, la valeur d'un drapeau
// nommé — pour scoper `--host` à l'environnement que `--env` a déjà posé plus
// tôt sur la même ligne.
func valeurDrapeau(avant []string, nom string) string {
	for i := 0; i < len(avant)-1; i++ {
		if strings.HasPrefix(avant[i], "-") && strings.TrimLeft(avant[i], "-") == nom {
			return avant[i+1]
		}
	}
	return ""
}

// valeursDynamiques résout le serveur, construit un client au timeout court,
// et rend la liste que la source associée à `nom` sait produire.
//
// Tout échec — pas de serveur configuré, pas de session, serveur injoignable
// — est avalé en silence : proposer moins vaut mieux que faire attendre un
// Tab, ou pire, le faire échouer.
func valeursDynamiques(ctx context.Context, nom string, avant []string) []string {
	source, ok := sourcesDynamiques[nom]
	if !ok {
		return nil
	}

	adresse, err := adresseServeurPourCompletion()
	if err != nil {
		return nil
	}
	api, err := clientPourCompletion(adresse)
	if err != nil {
		return nil
	}

	ctxCourt, annule := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer annule()

	return source(ctxCourt, api, avant)
}

// adresseServeurPourCompletion applique la même précédence que
// `adresseServeurDans`, en tolérant l'absence plutôt qu'en la signalant : une
// complétion sans serveur configuré ne propose rien, elle ne se plaint pas.
func adresseServeurPourCompletion() (string, error) {
	if a := os.Getenv(EnvServeur); a != "" {
		return a, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	cfg, err := projet.Lire(cwd)
	if err != nil {
		return "", err
	}
	if cfg.Server == "" {
		return "", errors.New("aucune adresse de serveur")
	}
	return cfg.Server, nil
}

// clientPourCompletion construit un client au TIMEOUT COURT : la complétion
// doit rendre la main en une fraction de seconde, jamais bloquer un Tab sur
// un serveur injoignable comme le ferait le timeout de 30s par défaut.
func clientPourCompletion(adresse string) (*client.Client, error) {
	cfg := client.Config{BaseURL: adresse, Subject: os.Getenv(EnvSujet), Timeout: 1500 * time.Millisecond}
	if s, err := session.Charger(adresse); err == nil && !s.Expiree() {
		cfg.Token = s.Token
	}
	return client.New(cfg)
}

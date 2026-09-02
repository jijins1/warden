package admin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"regexp"
	"strings"
	"text/tabwriter"

	"github.com/jijins1/warden/internal/cli/internal/client"
	"github.com/jijins1/warden/internal/git"
)

// OptionsDepot décrit ce qu'on demande à `warden repo declare`.
//
// Les trois champs du descripteur sont FACULTATIFS : ce qui n'est pas donné est
// déduit du dépôt où l'on se trouve. C'est le point de la commande — un
// administrateur assis dans un dépôt devrait pouvoir dire « celui-là ».
type OptionsDepot struct {
	RepoDir string

	Name        string
	RemoteURL   string
	MainlineRef string

	// Remote est le remote git interrogé pour la déduction. `origin` par défaut.
	Remote string

	// Confirmer est appelée avec le descripteur composé, avant l'écriture.
	// Rendre false abandonne.
	//
	// La question n'est pas décorative : `mainline_ref` se déduit le plus souvent
	// de la branche COURANTE, et déclarer un dépôt depuis une branche de feature
	// épinglerait le mauvais point de comparaison pour tous ses runs.
	Confirmer func(client.Repo) bool
}

// remoteParDefaut est le remote interrogé quand aucun n'est nommé.
const remoteParDefaut = "origin"

// nomDeDepot reprend la contrainte `repo_name_forme` du schéma.
//
// Quatrième copie de cette expression dans le dépôt, et la duplication reste le
// motif en place : chaque couche couvre un chemin d'entrée différent, et celle-ci
// couvre le seul où le nom n'a été TAPÉ par personne — il est découpé d'une URL,
// donc il peut être n'importe quoi.
var nomDeDepot = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// DeclarerDepot enregistre le dépôt courant auprès du serveur.
func DeclarerDepot(ctx context.Context, api *client.Client, o OptionsDepot, sortie io.Writer) error {
	r, err := Detecter(ctx, o)
	if err != nil {
		return err
	}

	afficherDepot(sortie, r)

	if o.Confirmer != nil && !o.Confirmer(r) {
		return errors.New("déclaration abandonnée")
	}

	if err := api.PutRepo(ctx, r); err != nil {
		return fmt.Errorf("déclaration de %s : %w", r.Name, err)
	}

	_, _ = fmt.Fprintf(sortie, "\n%s est déclaré.\n", r.Name)

	// La ligne à coller, et non le fichier écrit à la place de l'utilisateur :
	// `.ansibleproxy.yml` est versionné et lu par le SERVEUR depuis l'arbre du
	// commit. Le modifier d'autorité, dans le dos d'un `git status`, serait la
	// mauvaise surprise exacte que ce produit existe pour éviter.
	_, _ = fmt.Fprintf(sortie, "\nPour que `warden submit` n'ait plus besoin de --repo, ajoutez à\n"+
		".ansibleproxy.yml :\n\n    repo: %s\n\n"+
		"Attendez pour cela que le SERVEUR accepte ce champ : il relit ce fichier\n"+
		"depuis l'arbre du commit et refuse les clés qu'il ne connaît pas.\n", r.Name)
	return nil
}

// Detecter compose le descripteur du dépôt, en complétant ce qui manque.
//
// Rien de ce qui est donné explicitement n'est déduit : les drapeaux l'emportent
// toujours, et la déduction ne sert qu'à ce qu'ils soient inutiles dans le cas
// courant.
func Detecter(ctx context.Context, o OptionsDepot) (client.Repo, error) {
	remote := o.Remote
	if remote == "" {
		remote = remoteParDefaut
	}

	g := git.New(o.RepoDir)
	if !g.IsRepository(ctx) {
		return client.Repo{}, fmt.Errorf("%s n'est pas un dépôt git — "+
			"placez-vous dans le dépôt à déclarer", o.RepoDir)
	}

	r := client.Repo{
		Name:        o.Name,
		RemoteURL:   o.RemoteURL,
		MainlineRef: o.MainlineRef,
	}

	if r.RemoteURL == "" {
		brut, err := g.RemoteURL(ctx, remote)
		if err != nil {
			return client.Repo{}, err
		}
		if brut == "" {
			return client.Repo{}, fmt.Errorf("ce dépôt n'a pas de remote « %s » — "+
				"donnez --remote-url, ou --remote pour en nommer un autre", remote)
		}
		r.RemoteURL = brut
	}

	// La validation vaut pour l'URL DÉDUITE comme pour celle qu'on donne : c'est
	// la même valeur qui partira au serveur, et le `ext::` d'un remote local est
	// exactement aussi exécutable que celui d'un fichier de configuration.
	propre, err := git.ParseRemote(r.RemoteURL)
	if err != nil {
		return client.Repo{}, fmt.Errorf("remote %s : %w\n\n"+
			"Le serveur ne sait cloner qu'en http(s). Une URL SSH — git@…, ssh://… —\n"+
			"désigne pourtant souvent le même dépôt : donnez sa forme http avec\n"+
			"--remote-url.", remote, err)
	}
	r.RemoteURL = propre

	if r.Name == "" {
		nom, err := nomDepuisURL(r.RemoteURL)
		if err != nil {
			return client.Repo{}, err
		}
		r.Name = nom
	}
	if !nomDeDepot.MatchString(r.Name) {
		return client.Repo{}, fmt.Errorf("nom de dépôt %q refusé — "+
			"lettres, chiffres, `.`, `_` et `-`, 128 caractères au plus ; "+
			"donnez --name", r.Name)
	}

	if r.MainlineRef == "" {
		ref, err := refPrincipale(ctx, g, remote)
		if err != nil {
			return client.Repo{}, err
		}
		r.MainlineRef = ref
	}
	if !strings.HasPrefix(r.MainlineRef, "refs/heads/") {
		return client.Repo{}, fmt.Errorf("mainline_ref %q : une ref de branche est "+
			"attendue, sous la forme refs/heads/<nom>", r.MainlineRef)
	}

	return r, nil
}

// nomDepuisURL découpe le dernier segment du chemin, sans son `.git`.
func nomDepuisURL(brut string) (string, error) {
	u, err := url.Parse(brut)
	if err != nil {
		return "", fmt.Errorf("URL de remote illisible : %w", err)
	}
	nom := strings.TrimSuffix(path.Base(strings.TrimSuffix(u.Path, "/")), ".git")
	if nom == "" || nom == "." || nom == "/" {
		return "", fmt.Errorf("aucun nom de dépôt à tirer de %s — donnez --name", brut)
	}
	return nom, nil
}

// refPrincipale déduit la branche de référence, dans cet ordre.
//
// La branche par défaut du REMOTE d'abord, la branche courante ensuite. L'ordre
// porte la correction : la branche courante est ce qu'on a sous la main, pas ce
// contre quoi les runs doivent être comparés. Elle ne sert que de repli, et la
// commande fait confirmer ce qu'elle a trouvé.
func refPrincipale(ctx context.Context, g *git.Git, remote string) (string, error) {
	ref, err := g.RemoteHeadRef(ctx, remote)
	if err != nil {
		return "", err
	}
	if ref != "" {
		return ref, nil
	}

	ref, err = g.HeadRef(ctx)
	if err != nil {
		return "", err
	}
	if ref == "" {
		return "", errors.New("HEAD est détachée et le remote n'annonce pas de " +
			"branche par défaut : donnez --mainline-ref")
	}
	return ref, nil
}

// ListerDepots affiche les dépôts connus du serveur.
func ListerDepots(ctx context.Context, api *client.Client, sortie io.Writer) error {
	depots, err := api.ListRepos(ctx)
	if err != nil {
		return err
	}
	if len(depots) == 0 {
		_, _ = fmt.Fprintln(sortie, "aucun dépôt déclaré")
		return nil
	}

	tw := tabwriter.NewWriter(sortie, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "DÉPÔT\tREMOTE\tBRANCHE DE RÉFÉRENCE")
	for _, d := range depots {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", d.Name, d.RemoteURL, d.MainlineRef)
	}
	_ = tw.Flush()
	return nil
}

func afficherDepot(w io.Writer, r client.Repo) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "nom\t%s\n", r.Name)
	_, _ = fmt.Fprintf(tw, "remote\t%s\n", r.RemoteURL)
	_, _ = fmt.Fprintf(tw, "branche de référence\t%s\n", r.MainlineRef)
	_ = tw.Flush()
}

// ConfirmerDepotAuTerminal demande l'accord sur le descripteur composé.
//
// Le refus est le DÉFAUT, comme pour une empreinte : ce qui est en jeu ici est
// la branche contre laquelle TOUS les runs du dépôt seront comparés.
func ConfirmerDepotAuTerminal(entree io.Reader, sortie io.Writer) func(client.Repo) bool {
	return func(r client.Repo) bool {
		_, _ = fmt.Fprintf(sortie, "\nDéclarer ce dépôt auprès du serveur ? [o/N] ")
		return lireOui(entree)
	}
}

package fetch

import (
	"context"
	"fmt"
	"os"

	"github.com/jijins1/warden/internal/git"
)

// RunBranch est la seule ref que le runner voit.
//
// Elle est unique parce que l'`upload-pack` qui la sert tourne avec
// GIT_NAMESPACE (ADR-0007) : ce n'est pas une convention entre le runner et le
// serveur, c'est une contrainte que git applique en dehors de la négociation. Un
// runner qui demanderait autre chose ne recevrait rien.
const RunBranch = "refs/heads/run"

// Materialize matérialise l'arbre du commit à exécuter dans workspace.
//
// Elle VÉRIFIE que ce qu'elle a obtenu est ce que l'ordre nommait, et c'est la
// raison d'être de cette fonction plutôt que d'un appel direct à git. Le §8.2
// veut que « ce qui s'exécute corresponde exactement au commit journalisé » ;
// sans cette comparaison, le runner exécuterait ce que le serveur lui a servi et
// l'audit enregistrerait ce que le serveur avait promis, deux choses qui
// coïncident tant que rien ne va mal.
//
// La vérification est bon marché et son absence serait indétectable — c'est
// exactement le profil d'un contrôle qu'on omet.
func Materialize(ctx context.Context, workspace, remote, token string, attendu git.SHA) error {
	if workspace == "" {
		return fmt.Errorf("espace de travail non défini")
	}
	if !attendu.Valid() {
		return fmt.Errorf("commit à exécuter invalide : %q", attendu)
	}

	if err := os.MkdirAll(workspace, 0o700); err != nil {
		return fmt.Errorf("création de l'espace de travail : %w", err)
	}

	depot := git.New(workspace)
	if err := depot.Init(ctx); err != nil {
		return err
	}

	// Une ref et non le SHA : le transport de lecture épingle le protocole en v0
	// précisément pour qu'un objet non annoncé ne puisse pas être demandé par son
	// identifiant (ADR-0026). L'espace de noms rend exactement une ref visible.
	if err := depot.FetchShallowRef(ctx, remote, token, RunBranch); err != nil {
		return fmt.Errorf("récupération du code : %w", err)
	}

	obtenu, err := depot.Resolve(ctx, "FETCH_HEAD")
	if err != nil {
		return fmt.Errorf("résolution du commit récupéré : %w", err)
	}

	// LA vérification. Elle échoue plutôt que d'exécuter autre chose que ce que
	// l'audit enregistrera.
	if obtenu != attendu {
		return fmt.Errorf("le commit récupéré (%s) n'est pas celui de l'ordre (%s) : "+
			"exécution refusée", obtenu, attendu)
	}

	if err := depot.CheckoutDetached(ctx, attendu); err != nil {
		return err
	}
	return nil
}

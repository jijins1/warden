package fetch

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/jijins1/warden/internal/git"
)

// RunBranch est la seule ref que le runner voit.
//
// Elle est unique parce que l'`upload-pack` qui la sert tourne avec
// GIT_NAMESPACE (ADR-0007) : ce n'est pas une convention entre le runner et le
// serveur, c'est une contrainte que git applique en dehors de la négociation. Un
// runner qui demanderait autre chose ne recevrait rien.
const RunBranch = "refs/heads/run"

// Le barème des reprises du fetch initial.
//
// Il vient d'une MESURE sur le cluster et non d'un usage : le premier accès
// réseau d'un pod qui vient de démarrer est refusé en une milliseconde, et le
// même accès passe une seconde plus tard. Les règles que le CNI programme pour
// un pod neuf n'existent pas encore quand le conteneur, lui, tourne déjà.
//
// Quatre tentatives couvrent 3,5 s d'attente cumulée, soit largement la fenêtre
// observée, sans faire d'un rôle `git` éteint un pod qui s'éternise.
const (
	tentativesFetch = 4
	attenteInitiale = 500 * time.Millisecond
)

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
	// L'origine est validée ICI, avant toute tentative, parce que les reprises
	// plus bas ne classent pas les erreurs : une origine refusée — `ext::`, qui
	// exécuterait une commande — ne deviendra jamais acceptable, et la rejouer
	// n'ajouterait que du délai avant un message qui était déjà exact.
	//
	// C'est la seule classe d'échec qu'on sait trancher sans lire ce que git
	// écrit : elle se décide sur l'argument, pas sur le monde.
	if _, err := git.ParseRemote(remote); err != nil {
		return err
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
	if err := fetchAvecReprises(ctx, depot, remote, token); err != nil {
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

// fetchAvecReprises rejoue le fetch tant qu'il échoue, dans la limite du barème.
//
// AUCUNE ERREUR N'EST CLASSÉE, et c'est délibéré. Distinguer « le réseau n'est
// pas prêt » d'un « jeton refusé » demanderait de lire le texte que git écrit
// sur sa sortie d'erreur ; ce serait faire reposer une décision sur une chaîne
// de caractères que la version suivante de git peut réécrire. Le prix de ne pas
// classer est que quatre secondes s'écoulent avant qu'un jeton réellement
// invalide soit rapporté — un pod qui, de toute façon, ne fera rien d'autre.
//
// Le fetch est rejouable sans précaution : `--depth=1` dans un dépôt neuf n'a
// pas d'état partiel à reprendre, et FETCH_HEAD n'est lu qu'après le succès.
func fetchAvecReprises(ctx context.Context, depot *git.Git, remote, token string) error {
	attente := attenteInitiale
	var derniere error

	for essai := 1; essai <= tentativesFetch; essai++ {
		derniere = depot.FetchShallowRef(ctx, remote, token, RunBranch)
		if derniere == nil {
			return nil
		}
		if essai == tentativesFetch {
			break
		}

		// Le jeton ne paraît nulle part dans ce journal : l'erreur de git porte
		// l'adresse, et l'adresse seule.
		slog.Warn("récupération du code : nouvel essai",
			slog.Int("essai", essai), slog.Duration("attente", attente))

		select {
		case <-ctx.Done():
			// L'erreur du fetch est rendue plutôt que celle du contexte : elle
			// dit ce qui n'a pas marché, là où « context canceled » ne dirait que
			// quand on a cessé d'essayer.
			return derniere
		case <-time.After(attente):
		}
		attente *= 2
	}
	return derniere
}

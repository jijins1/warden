// Package auth porte les commandes de session de la CLI.
//
// Il ne décide RIEN sur l'authentification : la politique — longueur minimale,
// délai plancher, expiration — vit côté serveur, parce qu'un client modifié ne
// doit pas pouvoir la contourner. Ce paquet demande, transmet et range.
package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/jijins1/warden/internal/cli/internal/client"
	"github.com/jijins1/warden/internal/cli/internal/prompt"
	"github.com/jijins1/warden/internal/cli/internal/session"
)

// Login ouvre une session et range le jeton.
//
// Le mot de passe passe par le REGISTRE DE SAISIE commun (`prompt.Source`), le
// même que le mot de passe d'élévation et celui d'un enrôlement : jamais dans
// argv, jamais dans l'environnement, et refus net quand deux provenances sont
// données à la fois.
func Login(ctx context.Context, api *client.Client, serveur, subject string,
	mdp *prompt.Source, sortie io.Writer) error {

	if subject == "" {
		return errors.New("--subject est requis : c'est l'identifiant de votre compte")
	}

	motDePasse, err := mdp.Lire(ctx)
	if err != nil {
		return err
	}
	// Effacé dès la réponse obtenue. La copie que `client.Login` fait pour le
	// JSON est hors d'atteinte — Go n'efface pas une chaîne — mais ce n'est pas
	// une raison pour laisser traîner celle qu'on contrôle.
	defer motDePasse.Wipe()

	o, err := api.Login(ctx, subject, motDePasse.Reveal())
	if err != nil {
		return err
	}

	expire, err := time.Parse(time.RFC3339, o.ExpiresAt)
	if err != nil {
		// Une date illisible n'empêche PAS de ranger le jeton : il fonctionne, et
		// le refuser pour un champ d'affichage transformerait une bizarrerie en
		// panne. La session sera simplement considérée sans échéance connue.
		expire = time.Time{}
	}

	if err := session.Ranger(serveur, session.Session{
		Token: o.Token, Subject: o.Subject, ExpiresAt: expire,
	}); err != nil {
		return err
	}

	qui := o.Subject
	if o.Name != "" && o.Name != o.Subject {
		qui = fmt.Sprintf("%s (%s)", o.Name, o.Subject)
	}
	_, _ = fmt.Fprintf(sortie, "connecté sur %s en tant que %s", serveur, qui)
	if o.Admin {
		_, _ = fmt.Fprint(sortie, " — administrateur")
	}
	_, _ = fmt.Fprintln(sortie)
	if !expire.IsZero() {
		_, _ = fmt.Fprintf(sortie, "  session valide jusqu'au %s\n",
			expire.Local().Format("2006-01-02 15:04"))
	}
	return nil
}

// Logout révoque la session et oublie le jeton.
//
// Les DEUX, et dans cet ordre — mais l'échec du premier n'empêche pas le second.
// Un serveur injoignable ne doit pas laisser un jeton sur le poste alors que
// l'utilisateur a demandé à se déconnecter ; le jeton finira par expirer, et il
// vaut mieux un jeton orphelin côté serveur qu'un jeton actif sur un poste que
// son propriétaire croit propre.
func Logout(ctx context.Context, api *client.Client, serveur string, sortie io.Writer) error {
	var distant error
	if api != nil {
		distant = api.Logout(ctx)
	}

	if err := session.Oublier(serveur); err != nil {
		return err
	}

	if distant != nil {
		_, _ = fmt.Fprintf(sortie, "jeton oublié localement, mais le serveur "+
			"n'a pas pu le révoquer : %v\n", distant)
		_, _ = fmt.Fprintf(sortie, "  la session restera valide côté serveur "+
			"jusqu'à son expiration\n")
		return nil
	}
	_, _ = fmt.Fprintf(sortie, "déconnecté de %s\n", serveur)
	return nil
}

// Whoami demande au SERVEUR qui il croit que nous sommes.
//
// Au serveur et non au fichier local : le fichier dit ce qu'on a rangé, le
// serveur dit ce qui est encore valable. Toute la différence est là quand une
// session a été révoquée ailleurs — un administrateur qui coupe un compte, un
// mot de passe changé sur un autre poste.
func Whoami(ctx context.Context, api *client.Client, serveur string, sortie io.Writer) error {
	id, err := api.Whoami(ctx)
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintf(sortie, "%s sur %s\n", id.Subject, serveur)
	if id.Name != "" && id.Name != id.Subject {
		_, _ = fmt.Fprintf(sortie, "  nom       %s\n", id.Name)
	}
	if id.Admin {
		_, _ = fmt.Fprint(sortie, "  rôle      administrateur\n")
	}
	if id.ExpiresAt == "" {
		// Pas de session : l'appelant s'est authentifié par l'en-tête de
		// développement. Le DIRE, plutôt que d'afficher une identité qui a l'air
		// aussi solide qu'une vraie.
		_, _ = fmt.Fprint(sortie, "  session   aucune — identité affirmée, non vérifiée\n")
		return nil
	}
	if t, err := time.Parse(time.RFC3339, id.ExpiresAt); err == nil {
		_, _ = fmt.Fprintf(sortie, "  session   jusqu'au %s\n", t.Local().Format("2006-01-02 15:04"))
	}
	return nil
}

// Passwd change le mot de passe de l'appelant.
//
// L'ANCIEN est demandé, bien que la session soit ouverte : c'est le serveur qui
// l'exige, et la raison est qu'un poste laissé déverrouillé suffirait sinon à
// s'approprier le compte de façon durable.
//
// Le succès COUPE toutes les sessions, y compris celle qui vient de servir. La
// CLI oublie donc son jeton et le dit : le laisser ferait échouer la commande
// suivante sur un message d'authentification, très loin de la cause.
func Passwd(ctx context.Context, api *client.Client, serveur string,
	ancien, nouveau *prompt.Source, sortie io.Writer) error {

	vieux, err := ancien.Lire(ctx)
	if err != nil {
		return err
	}
	defer vieux.Wipe()

	neuf, err := nouveau.Lire(ctx)
	if err != nil {
		return err
	}
	defer neuf.Wipe()

	if err := api.ChangerMotDePasse(ctx, vieux.Reveal(), neuf.Reveal()); err != nil {
		return err
	}

	if err := session.Oublier(serveur); err != nil {
		return err
	}

	_, _ = fmt.Fprint(sortie, "mot de passe changé\n")
	_, _ = fmt.Fprint(sortie, "  toutes vos sessions ont été coupées — relancez `warden login`\n")
	return nil
}

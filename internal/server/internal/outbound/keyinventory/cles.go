package keyinventory

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/jijins1/warden/internal/server/internal/core"
)

// ErrPassphrase signale une clé privée protégée par une phrase secrète.
//
// Elle est refusée à l'IMPORT, et le motif est mesuré et non supposé :
// `ansible-runner` charge la clé dans un `ssh-agent` par `ssh-add` avant de
// lancer `ansible-playbook`. Une clé protégée y ferait attendre une saisie qui
// ne viendra jamais dans un pod — le run resterait bloqué jusqu'à
// `activeDeadlineSeconds`, et le symptôme serait un run qui ne démarre pas,
// sans message.
//
// La refuser ici la fait échouer au moment où quelqu'un est devant son
// terminal, avec un message qui dit quoi faire.
var ErrPassphrase = errors.New("la clé est protégée par une phrase secrète")

// publiqueDe dérive la partie publique et l'empreinte d'une clé privée.
//
// Elle vérifie du même coup que ce qu'on nous donne EST une clé privée
// utilisable. Sans ce contrôle, un fichier quelconque serait accepté à l'import,
// écrit dans le Secret, et découvert invalide au premier run.
func publiqueDe(privee []byte) (publique, empreinte string, err error) {
	signataire, err := ssh.ParsePrivateKey(privee)

	var manque *ssh.PassphraseMissingError
	if errors.As(err, &manque) {
		return "", "", ErrPassphrase
	}
	if err != nil {
		// Le message d'origine n'est PAS enveloppé : il vient d'un analyseur qui
		// peut citer des octets de la clé, et cette erreur remonte jusqu'à des
		// traces.
		return "", "", errors.New("clé privée illisible : ni OpenSSH, ni PEM reconnu")
	}

	pub := signataire.PublicKey()
	return ligneAutorisee(pub), ssh.FingerprintSHA256(pub), nil
}

// ligneAutorisee rend la partie publique au format `authorized_keys`.
//
// Sans le saut de ligne final que `MarshalAuthorizedKey` ajoute : cette valeur
// va dans une colonne, dans une réponse JSON et sur un terminal, et un saut de
// ligne invisible en fin de chaîne se retrouve tôt ou tard collé au milieu d'un
// fichier `authorized_keys`.
func ligneAutorisee(pub ssh.PublicKey) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
}

// avecCommentaire ajoute le commentaire d'une ligne `authorized_keys`.
//
// Il n'a aucune valeur fonctionnelle et beaucoup de valeur d'exploitation :
// c'est ce qu'un administrateur voit en ouvrant le fichier sur une machine, et
// une clé anonyme dans un `authorized_keys` est une clé que personne n'ose
// retirer.
func avecCommentaire(ligne, environnement string) string {
	return fmt.Sprintf("%s warden-%s", ligne, environnement)
}

// frapper tire une paire ed25519 et rend la partie privée encodée, la ligne
// `authorized_keys` commentée et l'empreinte.
//
// ed25519 et non RSA : clé courte, signature rapide, et acceptée par tout
// OpenSSH depuis 6.5 (2014). Le choix ne se discute que si une cible refuse
// l'algorithme, ce qui se verrait à l'enrôlement et pas en production.
func frapper(environment string) (privee []byte, publique, empreinte string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, "", "", fmt.Errorf("frappe de la clé de %s: %w", environment, err)
	}

	bloc, err := ssh.MarshalPrivateKey(priv, "warden "+environment)
	if err != nil {
		return nil, "", "", fmt.Errorf("encodage de la clé de %s: %w", environment, err)
	}

	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return nil, "", "", fmt.Errorf("partie publique de %s: %w", environment, err)
	}

	return pem.EncodeToMemory(bloc),
		avecCommentaire(ligneAutorisee(sshPub), environment),
		ssh.FingerprintSHA256(sshPub), nil
}

// cataloguer écrit le descripteur PUIS le relit.
//
// Le descripteur est RELU après écriture, et ce n'est pas de la
// superstition : `created_at` et `rotated_at` sont posées par la BASE, donc
// celui qu'on vient de composer les porte à zéro. Le rendre tel quel
// affichait « posée le 0001-01-01 » à l'utilisateur.
//
// Une relecture qui échoue ne fait pas échouer la pose : la clé EST
// installée, et prétendre le contraire enverrait l'opérateur la reposer.
func cataloguer(ctx context.Context, catalogue core.KeyCatalog, d core.KeyDescriptor) (core.KeyDescriptor, error) {
	if err := catalogue.SaveKey(ctx, d); err != nil {
		return core.KeyDescriptor{}, err
	}
	if relu, err := catalogue.KeyByEnvironment(ctx, d.Environment); err == nil {
		return relu, nil
	}
	return d, nil
}

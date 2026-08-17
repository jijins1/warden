package keyinventory

import (
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"
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

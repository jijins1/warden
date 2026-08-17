// Package prompt lit les valeurs sensibles saisies par l'utilisateur.
//
// Il existe parce que trois chemins du produit ont le même besoin et ne doivent
// pas le résoudre chacun à sa façon : le mot de passe d'un enrôlement assisté,
// la clé privée d'un enrôlement déclaratif, et le mot de passe de `become`
// d'Ansible. Une saisie qui fuit est une saisie qui fuit partout.
//
// Domaine de confiance : la machine de l'utilisateur (ADR-0011). Ce que ce
// paquet protège n'est donc pas le serveur, c'est la valeur elle-même — contre
// l'historique du shell, contre la table des processus, contre les traces.
//
// # Ce qu'il ne fait jamais
//
// Ni `argv`, ni environnement. La ligne de commande d'un processus est lisible
// par tout utilisateur de la machine, et l'environnement se retrouve dans les
// traces d'incident — c'est mesuré ailleurs dans ce dépôt : la trame de
// démarrage d'`ansible-runner` recopie l'environnement complet du processus.
// C'est la même raison qui fait voyager le CHEMIN du jeton d'amorçage du runner
// plutôt que le jeton (ADR-0023).
package prompt

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/term"
)

// maxSecret borne ce qu'on accepte de lire.
//
// Une clé privée fait quelques kilo-octets ; un mot de passe, quelques dizaines
// d'octets. La borne existe pour qu'un `warden key import < /dev/urandom` échoue
// au lieu de faire grossir le processus jusqu'à sa mort.
const maxSecret = 64 << 10 // 64 Kio

// Secret est une valeur sensible saisie par l'utilisateur.
//
// Le masquage porte sur `String` et `GoString`, PAS sur `MarshalJSON`, et cette
// asymétrie est délibérée. C'est le patron de `wire/dispatch.Secret` et non
// celui de `core.PodToken` : le jeton d'amorçage ne se sérialise jamais
// légitimement, alors qu'un mot de passe de `become` DOIT pouvoir voyager vers
// le serveur. Masquer sa sérialisation enverrait le marqueur à la place de la
// valeur, en silence — le pire mode de défaillance disponible.
//
// Ce que le masquage couvre est donc l'autre famille : les `%v`, `%s`, `%+v` et
// `%#v` posés dans une trace, qui sont le chemin par lequel un secret arrive
// dans un journal sans que personne l'ait voulu.
type Secret []byte

// nolint:gosec
const marqueur = "[valeur saisie supprimée]"

// String masque la valeur. C'est ce qui protège des `%s` et des `%v`.
func (s Secret) String() string { return marqueur }

// GoString masque la valeur sous `%#v`, que String ne couvre pas.
func (s Secret) GoString() string { return marqueur }

// Reveal rend la valeur en clair.
//
// Nom délibérément inconfortable, comme ses deux jumeaux du dépôt : il doit se
// voir en revue.
func (s Secret) Reveal() []byte { return []byte(s) }

// Zero dit si la valeur est vide.
func (s Secret) Zero() bool { return len(s) == 0 }

// Wipe écrase la valeur en mémoire.
//
// Effet RÉEL mais partiel, et il faut le dire : le ramasse-miettes de Go a pu
// recopier ce tampon avant cet appel, et rien ici n'atteint les copies. Ce que
// Wipe raccourcit est la fenêtre pendant laquelle la valeur d'origine traîne
// dans un processus qui va continuer de tourner — ce n'est pas une garantie
// d'effacement.
func (s Secret) Wipe() {
	for i := range s {
		s[i] = 0
	}
}

// ErrPasDeTerminal signale qu'aucune saisie interactive n'est possible.
var ErrPasDeTerminal = errors.New("pas de terminal pour la saisie")

// ErrVide signale une saisie vide.
var ErrVide = errors.New("saisie vide")

// AskStdin demande une valeur sur l'entrée standard.
func AskStdin(ctx context.Context, invite string) (Secret, error) {
	return Ask(ctx, os.Stdin, invite)
}

// Ask demande une valeur sur un terminal, sans écho.
//
// Le terminal est passé en paramètre et non pris dans `os.Stdin` : c'est ce qui
// rend l'extinction de l'écho VÉRIFIABLE par un test, sur un pseudo-terminal.
// Une propriété de sécurité qu'aucun test ne peut atteindre est une intention.
//
// Elle ÉCHOUE quand l'entrée n'est pas un terminal, au lieu de se rabattre
// silencieusement sur une lecture ordinaire : sans cette règle, un `warden … <
// fichier` lirait la première ligne du fichier comme un mot de passe et
// personne ne s'en apercevrait. Les appelants qui veulent une saisie non
// interactive passent par ReadLine, explicitement.
func Ask(ctx context.Context, tty *os.File, invite string) (Secret, error) {
	if tty == nil {
		return nil, ErrPasDeTerminal
	}
	fd := int(tty.Fd())
	if !term.IsTerminal(fd) {
		return nil, ErrPasDeTerminal
	}

	// L'état est sauvegardé AVANT et restauré quoi qu'il arrive. `ReadPassword`
	// le fait déjà pour son propre retour, mais pas pour le chemin
	// d'annulation — et un terminal laissé sans écho est un shell inutilisable
	// jusqu'à `stty sane`, ce que personne ne devine.
	etat, err := term.GetState(fd)
	if err != nil {
		return nil, fmt.Errorf("état du terminal illisible : %w", err)
	}
	defer func() { _ = term.Restore(fd, etat) }()

	if _, err := fmt.Fprint(tty, invite); err != nil {
		return nil, fmt.Errorf("invite non écrite : %w", err)
	}

	type resultat struct {
		v   []byte
		err error
	}
	// Une goroutine, parce que la lecture d'un terminal ne s'interrompt pas.
	// Elle survit à l'annulation et se termine à la frappe suivante ou à la fin
	// du processus ; le tampon a une case pour qu'elle ne reste pas bloquée à
	// l'envoi. C'est acceptable dans une CLI qui va se terminer, et ce serait
	// inacceptable dans un service.
	fini := make(chan resultat, 1)
	go func() {
		v, err := term.ReadPassword(fd)
		fini <- resultat{v, err}
	}()

	select {
	case <-ctx.Done():
		// Le `defer` restaure l'écho ; le saut de ligne remet le curseur en
		// début de ligne, sinon l'invite reste collée au message d'erreur.
		_, _ = fmt.Fprintln(tty)
		return nil, ctx.Err()

	case r := <-fini:
		_, _ = fmt.Fprintln(tty)
		if r.err != nil {
			// L'erreur du terminal est enveloppée telle quelle : elle ne peut
			// pas contenir la valeur, `ReadPassword` ne la rend qu'en retour.
			return nil, fmt.Errorf("saisie interrompue : %w", r.err)
		}
		if len(r.v) == 0 {
			return nil, ErrVide
		}
		return Secret(r.v), nil
	}
}

// ReadLine lit UNE ligne, sans terminal.
//
// C'est le chemin non interactif — un `--…-stdin` à la manière de
// `docker login --password-stdin` — et il est explicite par construction :
// l'appelant a demandé cette lecture, elle ne peut donc pas se produire par
// accident sur une entrée redirigée.
//
// Le saut de ligne final est retiré, `\r` compris : une valeur saisie depuis un
// fichier écrit sous Windows porterait sinon un caractère invisible, et le
// symptôme serait une authentification refusée sans raison lisible.
func ReadLine(r io.Reader) (Secret, error) {
	if r == nil {
		return nil, errors.New("aucune source de saisie")
	}
	// Le lecteur est borné ici aussi : une « ligne » sans saut de ligne est un
	// flux entier.
	br := bufio.NewReader(io.LimitReader(r, maxSecret+1))

	ligne, err := br.ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("lecture de la saisie : %w", err)
	}
	if len(ligne) > maxSecret {
		return nil, fmt.Errorf("saisie au-delà du plafond de %d octets", maxSecret)
	}

	ligne = bytes.TrimRight(ligne, "\r\n")
	if len(ligne) == 0 {
		return nil, ErrVide
	}
	return Secret(ligne), nil
}

// ReadAll lit tout, sans terminal.
//
// Pour ce qui est multi-ligne par nature — une clé privée au format PEM. Une
// invite sans écho conviendrait mal : l'utilisateur ne verrait rien de ce qu'il
// colle, et ne pourrait pas savoir où il en est.
func ReadAll(r io.Reader) (Secret, error) {
	if r == nil {
		return nil, errors.New("aucune source de saisie")
	}
	// LimitReader à plafond+1 : lire une octet de plus est ce qui permet de
	// DISTINGUER « pile au plafond » de « au-delà », donc de refuser au lieu de
	// tronquer en silence une clé privée.
	brut, err := io.ReadAll(io.LimitReader(r, maxSecret+1))
	if err != nil {
		return nil, fmt.Errorf("lecture de la saisie : %w", err)
	}
	if len(brut) > maxSecret {
		return nil, fmt.Errorf("saisie au-delà du plafond de %d octets", maxSecret)
	}

	brut = bytes.TrimRight(brut, "\r\n")
	if len(brut) == 0 {
		return nil, ErrVide
	}
	return Secret(brut), nil
}

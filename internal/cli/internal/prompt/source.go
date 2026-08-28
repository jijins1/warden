package prompt

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"golang.org/x/term"
)

// Source dit D'OÙ vient une valeur sensible.
//
// Chaque commande qui en demande une pose la même question — invite, fichier,
// ou entrée standard ? — et chacune y répondait à sa façon. Ce type porte la
// réponse une fois. Ce qui est en jeu n'est pas l'économie de lignes : ce sont
// des règles de sécurité, et une règle répétée à quatre endroits est une règle
// qu'un cinquième endroit oubliera.
//
// # Les valeurs attendues
//
// Aujourd'hui : la clé privée de `warden key import`, le mot de passe de
// `warden enroll`. Demain, et c'est pour elles que ce type existe : le mot de
// passe de `become`, celui d'un vault Ansible, et les `--extra-vars` sensibles
// que le §5.4 fait venir du shell de l'utilisateur.
//
// # Ce qui ne changera pas
//
// Ni `argv`, ni environnement — jamais, pour aucune de ces valeurs. La ligne de
// commande d'un processus est lisible par tout utilisateur de la machine, et
// l'environnement se retrouve dans les traces. Il n'y a donc pas de
// `--mot-de-passe <valeur>`, et il n'y en aura pas.
type Source struct {
	// Quoi nomme la valeur dans les messages : « le mot de passe du compte »,
	// « la clé privée ». C'est ce que l'utilisateur lit quand quelque chose
	// manque, donc ça décrit la CHOSE, pas le drapeau.
	Quoi string

	// Multiligne interdit la saisie interactive.
	//
	// Une clé privée au format PEM ne se colle pas à l'aveugle : l'utilisateur
	// ne verrait rien de ce qu'il tape et ne saurait pas où il en est. Pour
	// celles-là, un fichier ou l'entrée standard, et un message qui le dit.
	Multiligne bool

	// Les trois provenances, exclusives.
	Demande bool   // --ask-<nom>
	Fichier string // --<nom>-file
	Stdin   bool   // --<nom>-stdin
}

// Declarer enregistre les drapeaux d'une valeur sensible sur un FlagSet.
//
// Trois drapeaux nommés de la même façon partout : `--ask-x`, `--x-file`,
// `--x-stdin`. L'uniformité est le point — un utilisateur qui a appris
// `--key-file` sait déjà écrire `--password-file`.
//
// Rend la Source, qui n'est lisible qu'après `fs.Parse`.
func Declarer(fs *flag.FlagSet, nom, quoi string) *Source {
	s := &Source{Quoi: quoi}
	fs.BoolVar(&s.Demande, "ask-"+nom, false, "demander "+quoi+" au terminal")
	fs.StringVar(&s.Fichier, nom+"-file", "", "lire "+quoi+" dans un fichier")
	fs.BoolVar(&s.Stdin, nom+"-stdin", false, "lire "+quoi+" sur l'entrée standard")
	return s
}

// DeclarerMultiligne fait de même pour une valeur qui ne se saisit pas.
func DeclarerMultiligne(fs *flag.FlagSet, nom, quoi string) *Source {
	s := Declarer(fs, nom, quoi)
	s.Multiligne = true
	return s
}

// Abreger ajoute une abréviation à `--ask-<nom>`.
//
// Elle existe pour UNE raison, et l'uniformité des trois drapeaux ci-dessus est
// justement ce qui la rend nécessaire : quand l'outil qu'on remplace a déjà une
// abréviation établie, s'en écarter coûte plus cher que l'uniformité ne
// rapporte. `-K` est le `--ask-become-pass` d'`ansible-playbook` depuis
// toujours, et le §5.1 fait de la familiarité l'objectif d'expérience du
// produit — un réflexe qui échoue est ce qu'il existe pour éviter.
//
// Le MÊME booléen que `--ask-<nom>` : les deux formes sont interchangeables, et
// les donner toutes deux n'est pas une erreur.
func (s *Source) Abreger(fs *flag.FlagSet, lettre, quoi string) {
	fs.BoolVar(&s.Demande, lettre, false, quoi)
}

// Fournie dit si l'appelant a désigné une provenance.
//
// Utile pour les valeurs FACULTATIVES — le mot de passe de `become` n'est
// demandé que si on l'a réclamé — là où les valeurs obligatoires se contentent
// de laisser `Lire` échouer.
func (s *Source) Fournie() bool {
	return s.Demande || s.Fichier != "" || s.Stdin
}

// Lire résout la provenance et rend la valeur.
//
// Quand AUCUNE provenance n'est donnée, l'invite est prise par défaut si
// l'entrée standard est un terminal. C'est le comportement de `ssh` et de
// `sudo`, et il est sûr : ce qui est dangereux n'est pas de demander à un
// humain, c'est de lire une entrée redirigée comme si c'était une saisie. Sans
// terminal, la commande ÉCHOUE en nommant les deux drapeaux qui conviennent.
func (s *Source) Lire(ctx context.Context) (Secret, error) {
	if err := s.exclusives(); err != nil {
		return nil, err
	}

	switch {
	case s.Fichier != "":
		return s.depuisFichier()
	case s.Stdin:
		return ReadAll(os.Stdin)
	case s.Demande:
		return s.demander(ctx)
	}

	// Aucune provenance : l'invite, si elle est possible.
	if s.Multiligne {
		return nil, fmt.Errorf("%s doit venir d'un fichier ou de l'entrée standard "+
			"(elle est trop longue pour être saisie à l'aveugle)", s.Quoi)
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return nil, fmt.Errorf("%s est requis, et il n'y a pas de terminal pour le demander "+
			"— utiliser un fichier ou l'entrée standard", s.Quoi)
	}
	return s.demander(ctx)
}

// exclusives refuse deux provenances à la fois.
//
// Choisir silencieusement entre deux serait le pire des deux mondes : la
// commande réussirait, et personne ne saurait laquelle a servi.
func (s *Source) exclusives() error {
	n := 0
	for _, choisi := range []bool{s.Demande, s.Fichier != "", s.Stdin} {
		if choisi {
			n++
		}
	}
	if n > 1 {
		return fmt.Errorf("une seule provenance à la fois pour %s", s.Quoi)
	}
	return nil
}

func (s *Source) demander(ctx context.Context) (Secret, error) {
	if s.Multiligne {
		return nil, fmt.Errorf("%s ne se saisit pas au terminal : "+
			"l'utiliser depuis un fichier ou l'entrée standard", s.Quoi)
	}
	return AskStdin(ctx, s.Quoi+" : ")
}

func (s *Source) depuisFichier() (Secret, error) {
	f, err := os.Open(s.Fichier) // #nosec G304 -- chemin fourni par l'opérateur, c'est l'objet du drapeau
	if err != nil {
		return nil, fmt.Errorf("lecture de %s : %w", s.Quoi, err)
	}
	defer func() { _ = f.Close() }()

	secret, err := ReadAll(f)
	if errors.Is(err, ErrVide) {
		// Le message générique dirait « saisie vide » ; celui-ci nomme le
		// fichier, qui est ce que l'utilisateur doit aller regarder.
		return nil, fmt.Errorf("%s : %s est vide", s.Quoi, s.Fichier)
	}
	return secret, err
}

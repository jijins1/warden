// Package projet lit le fichier de configuration du dépôt de l'utilisateur.
//
// # Cette lecture n'est PAS autoritaire
//
// C'est le point qu'il faut avoir en tête en lisant ce paquet. L'ADR-0009 pose
// que la configuration effective d'un run est résolue par le SERVEUR, depuis
// l'arbre du commit d'audit — de sorte que ce qui s'applique et ce qui est
// audité soient le même octet. Rien de ce qui est lu ici ne s'impose au serveur.
//
// Alors pourquoi lire quand même ? Pour trois choses que le serveur ne peut pas
// faire à notre place :
//
//  1. `server` — il faut joindre quelqu'un avant qu'un commit existe de son côté.
//     C'est l'exception structurelle que l'ADR-0009 nomme ;
//  2. `forward_env` — les VALEURS ne peuvent venir que du shell de l'appelant, et
//     le serveur n'y a pas accès. Il faut donc savoir QUELS noms y chercher ;
//  3. `audit_include` — il décide de ce qui ENTRE dans le commit, donc le lire
//     depuis le commit serait circulaire. Seconde exception structurelle, que
//     l'ADR-0009 ne nomme pas.
//
// Pour tout le reste — `default_env`, `inventory`, `runner_image` — ce paquet
// n'est pas appelé, et c'est délibéré : une valeur lue ici puis envoyée dans la
// requête rouvrirait l'écart entre l'audité et l'appliqué.
package projet

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/jijins1/warden/internal/config"
)

// Lire charge le fichier depuis le répertoire du dépôt.
//
// L'ABSENCE n'est pas une erreur : la plupart des dépôts ne l'ont pas encore, et
// `warden submit` doit continuer d'y fonctionner.
//
// Un fichier PRÉSENT mais invalide, en revanche, échoue tout de suite. Le serveur
// le refuserait de toute façon en relisant le commit — mais après que
// l'utilisateur a capturé et poussé son arbre, ce qui lui fait payer un
// aller-retour pour une faute de frappe qu'on savait détecter ici.
func Lire(repoDir string) (config.Project, error) {
	chemin := filepath.Join(repoDir, config.File)

	brut, err := os.ReadFile(chemin) // #nosec G304 -- chemin composé du dépôt de l'appelant
	if errors.Is(err, fs.ErrNotExist) {
		return config.Project{}, nil
	}
	if err != nil {
		return config.Project{}, fmt.Errorf("lecture de %s : %w", config.File, err)
	}

	p, err := config.Parse(brut)
	if err != nil {
		// `config.Parse` nomme déjà la clé ou la construction refusée : le
		// reformuler la noierait.
		return config.Project{}, err
	}
	return p, nil
}

// Valeurs collecte dans le shell les valeurs des noms déclarés.
//
// SEULS les noms déclarés sont consultés. La CLI ne parcourt jamais
// l'environnement entier : ce qui n'est pas nommé dans un fichier versionné ne
// doit pas pouvoir partir, même par accident — et un `os.Environ()` recopié dans
// une requête est exactement le genre d'accident qui n'est découvert que dans un
// journal de proxy.
//
// `lookup` est injecté plutôt que d'appeler `os.LookupEnv` directement, pour que
// ce comportement soit vérifiable sans toucher à l'environnement du processus de
// test.
//
// La distinction entre ABSENTE et VIDE est conservée, et elle porte une décision.
// Une variable que le shell ne pose pas n'est pas transmise : poser une chaîne
// vide écraserait la valeur que la machine cible possède peut-être déjà. Une
// variable posée à la chaîne vide, elle, est une déclaration délibérée et passe
// telle quelle.
func Valeurs(declares []string, lookup func(string) (string, bool)) map[string]string {
	if len(declares) == 0 {
		return nil
	}

	out := make(map[string]string, len(declares))
	for _, nom := range declares {
		if v, pose := lookup(nom); pose {
			out[nom] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

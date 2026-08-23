// Package session range le jeton de `warden login` sur le poste.
//
// Domaine de confiance : la machine de l'utilisateur (ADR-0011). Ce jeton est la
// seule chose du produit qui y séjourne, et il vaut l'identité de son
// propriétaire — d'où les trois règles qui gouvernent ce paquet :
//
//   - un FICHIER, jamais une variable d'environnement : l'environnement d'un
//     processus fuit dans les traces, les rapports de plantage et les scripts
//     shell qu'on partage ;
//   - 0600 sur le fichier, 0700 sur le répertoire, VÉRIFIÉS à la lecture. Poser
//     les droits à l'écriture ne suffit pas : un umask permissif ou une copie
//     maladroite les relâchent, et personne ne le remarque ;
//   - indexé par ADRESSE DE SERVEUR. On parle à un warden de développement et à
//     un de production, et écraser l'un avec l'autre ferait pointer une commande
//     sur le mauvais parc.
package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ErrPasDeSession dit qu'aucun jeton n'est rangé pour ce serveur.
var ErrPasDeSession = errors.New("aucune session ouverte")

// ErrDroitsTropLarges refuse un fichier lisible par d'autres.
var ErrDroitsTropLarges = errors.New("droits trop larges sur le fichier d'identifiants")

// nomFichier est le fichier qui porte les jetons.
const nomFichier = "credentials.json"

// Session est ce qui est rangé pour un serveur donné.
type Session struct {
	// Token est en clair dans le fichier. Le chiffrer supposerait une clé, qui
	// devrait vivre quelque part sur la même machine — c'est-à-dire déplacer le
	// problème en donnant l'impression de le résoudre. Ce qui protège ce fichier
	// est le mode 0600, comme pour une clé SSH.
	Token string `json:"token"`

	Subject   string    `json:"subject"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Expiree dit si la session est passée.
//
// La marge n'est PAS du zèle : une session qui expire pendant un run ferait
// échouer une soumission à mi-chemin, et le message parlerait d'authentification
// alors que le vrai sujet est le calendrier.
func (s Session) Expiree() bool {
	return !s.ExpiresAt.IsZero() && time.Now().Add(time.Minute).After(s.ExpiresAt)
}

// fichier est la forme sur disque : les sessions, par serveur.
type fichier struct {
	Servers map[string]Session `json:"servers"`
}

// Charger lit la session rangée pour ce serveur.
//
// L'absence de fichier est ErrPasDeSession et non une erreur d'entrée-sortie :
// n'avoir jamais lancé `warden login` est l'état initial normal, et le message
// doit dire quoi faire plutôt que quel appel système a échoué.
func Charger(serveur string) (Session, error) {
	chemin, err := Chemin()
	if err != nil {
		return Session{}, err
	}

	info, err := os.Stat(chemin)
	if errors.Is(err, fs.ErrNotExist) {
		return Session{}, ErrPasDeSession
	}
	if err != nil {
		return Session{}, fmt.Errorf("lecture de %s : %w", chemin, err)
	}

	// Le contrôle des droits est à la LECTURE et pas seulement à l'écriture : un
	// umask permissif, une restauration de sauvegarde ou un `cp` sans `-p` les
	// relâchent, et le fichier continue de fonctionner sans que rien ne le dise.
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		return Session{}, fmt.Errorf("%w : %s est en %#o, attendu 0600 — "+
			"corrigez avec `chmod 600 %s`", ErrDroitsTropLarges, chemin, mode, chemin)
	}

	brut, err := os.ReadFile(chemin) // #nosec G304 -- chemin composé par Chemin()
	if err != nil {
		return Session{}, fmt.Errorf("lecture de %s : %w", chemin, err)
	}

	var f fichier
	if err := json.Unmarshal(brut, &f); err != nil {
		return Session{}, fmt.Errorf("%s est illisible : %w", chemin, err)
	}

	s, ok := f.Servers[normaliser(serveur)]
	if !ok || s.Token == "" {
		return Session{}, ErrPasDeSession
	}
	return s, nil
}

// Ranger écrit la session de ce serveur, sans toucher aux autres.
func Ranger(serveur string, s Session) error {
	chemin, err := Chemin()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(chemin), 0o700); err != nil {
		return fmt.Errorf("création de %s : %w", filepath.Dir(chemin), err)
	}

	f := lireOuVide(chemin)
	if f.Servers == nil {
		f.Servers = map[string]Session{}
	}
	f.Servers[normaliser(serveur)] = s

	return ecrire(chemin, f)
}

// Oublier retire la session de ce serveur.
//
// Elle ne renvoie PAS d'erreur quand il n'y en avait pas : `warden logout` est
// souvent lancé deux fois, et la seconde ne doit pas ressembler à une panne.
func Oublier(serveur string) error {
	chemin, err := Chemin()
	if err != nil {
		return err
	}

	f := lireOuVide(chemin)
	if f.Servers == nil {
		return nil
	}
	if _, présente := f.Servers[normaliser(serveur)]; !présente {
		return nil
	}
	delete(f.Servers, normaliser(serveur))

	return ecrire(chemin, f)
}

// ecrire remplace le fichier ATOMIQUEMENT.
//
// Écriture dans un temporaire du même répertoire puis renommage : une écriture
// en place interrompue laisserait un JSON tronqué, donc toutes les sessions
// perdues d'un coup — y compris celles des serveurs qu'on ne touchait pas.
func ecrire(chemin string, f fichier) error {
	brut, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("sérialisation des identifiants : %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(chemin), ".credentials-*")
	if err != nil {
		return fmt.Errorf("écriture des identifiants : %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // sans effet après le renommage

	// Les droits AVANT le contenu : entre la création et le chmod, le fichier
	// existe avec les droits que le umask a décidés. Y écrire un jeton d'abord
	// ouvrirait une fenêtre, si courte soit-elle.
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("droits sur les identifiants : %w", err)
	}
	if _, err := tmp.Write(brut); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("écriture des identifiants : %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("écriture des identifiants : %w", err)
	}
	if err := os.Rename(tmp.Name(), chemin); err != nil {
		return fmt.Errorf("écriture des identifiants : %w", err)
	}
	return nil
}

// lireOuVide rend le contenu, ou une structure vide.
//
// Un fichier illisible est traité comme absent, et c'est un choix : refuser
// bloquerait `warden login` sur un fichier corrompu, alors que se reconnecter
// est précisément ce qui le répare.
func lireOuVide(chemin string) fichier {
	brut, err := os.ReadFile(chemin) // #nosec G304 -- chemin composé par Chemin()
	if err != nil {
		return fichier{}
	}
	var f fichier
	if err := json.Unmarshal(brut, &f); err != nil {
		return fichier{}
	}
	return f
}

// Chemin rend l'emplacement du fichier d'identifiants.
//
// `os.UserConfigDir` respecte `XDG_CONFIG_HOME` sous Linux, ce qui rend le
// chemin déplaçable en test sans variable inventée pour l'occasion.
func Chemin() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("répertoire de configuration introuvable : %w", err)
	}
	return filepath.Join(base, "warden", nomFichier), nil
}

// normaliser rend deux écritures de la même adresse identiques.
//
// Sans elle, `https://warden.example` et `https://warden.example/` seraient deux
// entrées, et se connecter à l'une laisserait l'autre demander une connexion —
// une confusion d'autant plus pénible qu'elle est invisible.
func normaliser(serveur string) string {
	return strings.TrimRight(strings.TrimSpace(serveur), "/")
}

package core

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"golang.org/x/crypto/argon2"
)

// L'empreinte d'un mot de passe vit dans le cœur et non dans un adaptateur.
//
// Ce n'est pas une entorse à l'ADR-0019 mais son application : ce fichier ne
// connaît ni PostgreSQL, ni HTTP, ni Kubernetes — c'est du calcul pur, au même
// titre que le SHA-256 de `PodToken.Hash`. Ce qui appartiendrait à un adaptateur
// serait l'endroit où l'empreinte est RANGÉE, et c'est bien le magasin qui s'en
// charge.

// ErrMotDePasseFaible refuse un mot de passe trop court.
var ErrMotDePasseFaible = errors.New("mot de passe trop court")

// ErrEmpreinteIllisible signale une empreinte qu'on ne sait pas relire.
var ErrEmpreinteIllisible = errors.New("empreinte de mot de passe illisible")

// LongueurMinimale est le seul contrôle de qualité imposé.
//
// Douze caractères, et AUCUNE règle de composition. Les exigences de classes de
// caractères produisent « Motdepasse1! » — long en apparence, deviné en
// pratique — et poussent à écrire le mot de passe sur un papier. Le NIST
// SP 800-63B a retiré ces règles pour cette raison exacte.
//
// Le contrôle est ici et pas à la saisie : la CLI ne doit pas être l'endroit qui
// décide de la politique, sans quoi un client modifié la contournerait.
const LongueurMinimale = 12

// Paramètres argon2id, profil « deuxième option recommandée » de la RFC 9106.
//
// m=19 Mio plutôt que les 64 Mio du premier profil, et le choix se justifie par
// l'endroit où ça tourne : le rôle `api` est un pod, plusieurs connexions
// simultanées à 64 Mio chacune le feraient sortir par l'OOM — c'est-à-dire
// qu'un durcissement du hachage deviendrait un déni de service. La RFC donne
// explicitement ce profil comme équivalent en sécurité.
//
// Les valeurs sont écrites DANS l'empreinte (format PHC), donc les changer
// n'invalide rien : les anciennes empreintes restent vérifiables avec leurs
// propres paramètres.
const (
	argonMemoire    uint32 = 19 * 1024 // en Kio
	argonIterations uint32 = 2
	argonThreads    uint8  = 1
	argonLongueur   uint32 = 32
	argonSel        uint32 = 16
)

// Empreinte calcule l'empreinte argon2id d'un mot de passe, au format PHC.
func Empreinte(motDePasse []byte) (string, error) {
	if len(motDePasse) < LongueurMinimale {
		return "", fmt.Errorf("%w : %d caractères au minimum",
			ErrMotDePasseFaible, LongueurMinimale)
	}

	sel := make([]byte, argonSel)
	if _, err := rand.Read(sel); err != nil {
		return "", fmt.Errorf("tirage du sel : %w", err)
	}
	return composerPHC(sel, dériver(motDePasse, sel, argonMemoire, argonIterations, argonThreads)), nil
}

// VerifierMotDePasse dit si le mot de passe correspond à l'empreinte.
//
// L'erreur est distincte du FAUX : une empreinte illisible est un défaut de
// données, pas un mauvais mot de passe, et les confondre ferait passer une
// corruption pour un utilisateur qui se trompe de touche.
//
// La comparaison est à temps constant. Ce n'est pas du zèle : sans cela, le
// temps de réponse révèle combien d'octets de tête sont justes, ce qui rend
// l'empreinte devinable octet par octet.
func VerifierMotDePasse(empreinte string, motDePasse []byte) (bool, error) {
	sel, attendu, mem, iter, threads, err := lirePHC(empreinte)
	if err != nil {
		return false, err
	}

	calcule := dériver(motDePasse, sel, mem, iter, threads)
	return subtle.ConstantTimeCompare(calcule, attendu) == 1, nil
}

// EmpreinteLeurre est une empreinte valide dont aucun mot de passe n'est connu.
//
// Elle sert à un seul usage, et il compte : quand le sujet demandé n'existe pas,
// la vérification est faite QUAND MÊME contre cette valeur. Sans elle, un compte
// inconnu répondrait en une microseconde et un compte existant en cinquante
// millisecondes — de quoi énumérer les comptes du produit en interrogeant
// simplement l'endpoint de connexion.
//
// Calculée une fois au démarrage : la dériver à chaque tentative coûterait le
// même temps mais pour rien.
var EmpreinteLeurre = leurre()

func leurre() string {
	sel := make([]byte, argonSel) // des zéros : rien ici n'est secret
	return composerPHC(sel, dériver([]byte("aucun mot de passe ne correspond"),
		sel, argonMemoire, argonIterations, argonThreads))
}

func dériver(motDePasse, sel []byte, mem, iter uint32, threads uint8) []byte {
	return argon2.IDKey(motDePasse, sel, iter, mem, threads, argonLongueur)
}

func composerPHC(sel, empreinte []byte) string {
	b64 := base64.RawStdEncoding.EncodeToString
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemoire, argonIterations, argonThreads,
		b64(sel), b64(empreinte))
}

// lirePHC relit une empreinte et SES paramètres.
//
// Les paramètres sont relus de la chaîne et non repris des constantes : c'est
// exactement ce qui permet de durcir le coût de calcul sans invalider ce qui est
// déjà en base.
func lirePHC(s string) (sel, empreinte []byte, mem, iter uint32, threads uint8, err error) {
	parts := strings.Split(s, "$")
	// "", "argon2id", "v=19", "m=...,t=...,p=...", sel, empreinte
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return nil, nil, 0, 0, 0, fmt.Errorf("%w : format inattendu", ErrEmpreinteIllisible)
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return nil, nil, 0, 0, 0, fmt.Errorf("%w : version", ErrEmpreinteIllisible)
	}
	if version != argon2.Version {
		// Refuser plutôt que tenter : une version différente ne dérive pas la
		// même valeur, et « faux » se lirait comme un mauvais mot de passe.
		return nil, nil, 0, 0, 0, fmt.Errorf("%w : version %d inconnue", ErrEmpreinteIllisible, version)
	}

	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &mem, &iter, &threads); err != nil {
		return nil, nil, 0, 0, 0, fmt.Errorf("%w : paramètres", ErrEmpreinteIllisible)
	}
	if mem == 0 || iter == 0 || threads == 0 {
		return nil, nil, 0, 0, 0, fmt.Errorf("%w : paramètre nul", ErrEmpreinteIllisible)
	}

	if sel, err = base64.RawStdEncoding.DecodeString(parts[4]); err != nil {
		return nil, nil, 0, 0, 0, fmt.Errorf("%w : sel", ErrEmpreinteIllisible)
	}
	if empreinte, err = base64.RawStdEncoding.DecodeString(parts[5]); err != nil {
		return nil, nil, 0, 0, 0, fmt.Errorf("%w : empreinte", ErrEmpreinteIllisible)
	}
	if len(sel) == 0 || len(empreinte) == 0 {
		return nil, nil, 0, 0, 0, fmt.Errorf("%w : sel ou empreinte vide", ErrEmpreinteIllisible)
	}
	return sel, empreinte, mem, iter, threads, nil
}

// motsDePasseAlphabet exclut les caractères qu'on confond en les lisant.
//
// Ni `0`/`O`, ni `1`/`l`/`I`. Ce mot de passe est destiné à être TRANSMIS — lu à
// voix haute, recopié d'un écran, collé dans un message — et une ambiguïté de
// glyphe s'y paie en tentatives ratées qu'on prend pour une panne.
//
// Que des minuscules et des chiffres, aucun symbole : la robustesse vient de la
// longueur, pas de l'alphabet, et les symboles se perdent dans les copier-coller
// et les claviers d'un autre pays.
const motsDePasseAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"

// LongueurTiree est la longueur d'un mot de passe tiré par le serveur.
//
// Vingt caractères sur un alphabet de 31 : environ 99 bits d'entropie. Très
// au-delà de ce qu'exige argon2, et c'est voulu — ce mot de passe transite par
// un canal qu'on ne maîtrise pas (un message, un ticket), donc sa seule défense
// est d'être impossible à deviner et destiné à être remplacé.
const LongueurTiree = 20

// MotDePasseTire tire un mot de passe transmissible.
//
// Il est tiré par le SERVEUR et non choisi par l'administrateur, et c'est la
// décision qui compte : un mot de passe choisi par un tiers est un mot de passe
// que ce tiers connaît, et il est presque toujours faible parce qu'il doit être
// dicté.
func MotDePasseTire() (string, error) {
	out := make([]byte, LongueurTiree)
	for i := range out {
		// rand/v2 n'est pas utilisé ici : `crypto/rand.Int` évite le biais
		// modulo, qu'un `% len(alphabet)` sur un octet introduirait — 31 ne
		// divise pas 256, donc les premiers caractères de l'alphabet
		// sortiraient plus souvent.
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(motsDePasseAlphabet))))
		if err != nil {
			return "", fmt.Errorf("tirage du mot de passe: %w", err)
		}
		out[i] = motsDePasseAlphabet[n.Int64()]
	}
	return string(out), nil
}

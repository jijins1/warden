package miseajour

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/blake2b"
)

// La vérification d'une signature minisign, écrite ici plutôt que tirée de
// `go-minisign` : le format est court et figé, et la CLI ne prend pas une
// dépendance pour lui (ADR-0049).
//
// Une clé publique est `base64("Ed" ‖ id(8) ‖ clé(32))`. Une signature est un
// fichier de quatre lignes :
//
//	untrusted comment: <libre, NON signé>
//	base64(algo(2) ‖ id(8) ‖ signature(64))
//	trusted comment: <texte>
//	base64(signature_globale(64))
//
// `algo` vaut "ED" quand la signature porte sur le BLAKE2b-512 du fichier — le
// défaut de minisign depuis la 0.11 — et "Ed" quand elle porte sur le fichier
// lui-même. La signature globale porte sur `signature ‖ texte`, et c'est elle
// qui rend le commentaire de confiance… digne de confiance.

const (
	prefixeNonSigne = "untrusted comment:"
	prefixeSigne    = "trusted comment: "
)

// ErrSignature : la signature ne vérifie pas. Aucun détail n'est rendu au-delà
// de la cause : il n'y a rien à corriger côté utilisateur, sinon à ne pas
// installer ce fichier.
var ErrSignature = errors.New("signature invalide")

type clePublique struct {
	id  [8]byte
	cle ed25519.PublicKey
}

func lireClePublique(b64 string) (clePublique, error) {
	brut, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return clePublique{}, fmt.Errorf("clé publique illisible : %w", err)
	}
	if len(brut) != 2+8+ed25519.PublicKeySize || string(brut[:2]) != "Ed" {
		return clePublique{}, errors.New("clé publique : format minisign attendu")
	}
	var c clePublique
	copy(c.id[:], brut[2:10])
	c.cle = ed25519.PublicKey(brut[10:])
	return c, nil
}

// verifier contrôle `signature` sur `message` et rend le commentaire de
// confiance — qu'il appartient à l'appelant de comparer à ce qu'il attend.
func verifier(cle clePublique, message, signature []byte) (string, error) {
	lignes := strings.Split(strings.ReplaceAll(string(signature), "\r\n", "\n"), "\n")
	if len(lignes) < 4 ||
		!strings.HasPrefix(lignes[0], prefixeNonSigne) ||
		!strings.HasPrefix(lignes[2], prefixeSigne) {
		return "", fmt.Errorf("%w : format minisign attendu", ErrSignature)
	}

	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(lignes[1]))
	if err != nil || len(sig) != 2+8+ed25519.SignatureSize {
		return "", fmt.Errorf("%w : signature illisible", ErrSignature)
	}
	algo, id, signee := string(sig[:2]), sig[2:10], sig[10:]
	if !bytes.Equal(id, cle.id[:]) {
		return "", fmt.Errorf("%w : signée par une autre clé", ErrSignature)
	}

	switch algo {
	case "ED":
		empreinte := blake2b.Sum512(message)
		message = empreinte[:]
	case "Ed":
	default:
		return "", fmt.Errorf("%w : algorithme %q inconnu", ErrSignature, algo)
	}
	if !ed25519.Verify(cle.cle, message, signee) {
		return "", ErrSignature
	}

	texte := strings.TrimPrefix(lignes[2], prefixeSigne)
	globale, err := base64.StdEncoding.DecodeString(strings.TrimSpace(lignes[3]))
	if err != nil || len(globale) != ed25519.SignatureSize {
		return "", fmt.Errorf("%w : signature globale illisible", ErrSignature)
	}
	if !ed25519.Verify(cle.cle, append(append([]byte{}, signee...), texte...), globale) {
		return "", fmt.Errorf("%w : commentaire de confiance altéré", ErrSignature)
	}
	return texte, nil
}

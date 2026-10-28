// Package miseajour remplace le binaire de la CLI par la release d'une version
// donnée — celle du serveur (ADR-0049).
//
// Ce paquet ouvre un chemin d'exécution de code sur le poste : ce qu'il installe
// s'exécute au lancement suivant. Rien n'est donc installé qui n'ait passé, dans
// cet ordre, la signature minisign de `SHA256SUMS` par la clé embarquée, le
// commentaire de confiance qui lie cette signature à la version demandée, puis
// la somme du binaire lui-même.
package miseajour

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// BaseParDefaut est l'adresse des releases GitHub, où la CI publie la CLI.
//
// `WARDEN_UPDATE_URL` la remplace pour un miroir, qui reproduit l'arborescence
// `v<version>/…`. Le miroir n'a pas à être de confiance : la signature est
// vérifiée quel que soit l'hôte.
const BaseParDefaut = "https://github.com/jijins1/warden/releases/download"

// Tailles maximales lues. Une réponse qui les dépasse n'est pas une release.
const (
	tailleMaxSommes    = 64 << 10
	tailleMaxSignature = 4 << 10
	tailleMaxBinaire   = 256 << 20
)

// ErrSansCle : cette CLI n'embarque aucune clé de vérification.
var ErrSansCle = errors.New("cette CLI n'embarque pas de clé de vérification des releases : " +
	"elle ne peut pas se mettre à jour seule — réinstallez-la à la main")

// ErrNonInscriptible : le répertoire du binaire n'est pas inscriptible.
//
// La CLI n'élève pas ses droits pour se remplacer : un `sudo` caché dans une
// mise à jour serait exactement le genre de surprise que l'ADR-0049 écarte.
var ErrNonInscriptible = errors.New("le binaire ne peut pas être remplacé")

// versionValide borne ce que le serveur peut nous faire mettre dans une URL.
var versionValide = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+-]*$`)

// Options décrit une mise à jour.
type Options struct {
	// Version est la version visée, sans le `v` du tag.
	Version string

	// BaseURL est la base des releases. Vide : BaseParDefaut.
	BaseURL string

	// ClePublique est la clé minisign qui doit avoir signé la release.
	ClePublique string

	// OS et Arch désignent le binaire à télécharger (GOOS, GOARCH).
	OS, Arch string

	// Executable est le chemin du binaire à remplacer.
	Executable string

	HTTP *http.Client
}

// NomDuBinaire est le nom sous lequel la CI publie la CLI pour une cible.
func NomDuBinaire(goos, goarch string) string {
	nom := "warden_" + goos + "_" + goarch
	if goos == "windows" {
		nom += ".exe"
	}
	return nom
}

// CheminDuBinaire rend le chemin RÉEL du binaire en cours d'exécution.
//
// Les liens sont résolus : remplacer le lien laisserait l'ancien binaire en
// place, et la commande relancée serait la même que celle qui a demandé la mise
// à jour.
func CheminDuBinaire() (string, error) {
	chemin, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("chemin du binaire introuvable : %w", err)
	}
	return filepath.EvalSymlinks(chemin)
}

// Installer télécharge, vérifie et met en place la version demandée.
//
// Le binaire courant n'est touché qu'en dernier, par un renommage : tant que
// toutes les vérifications ne sont pas passées, il n'y a rien à défaire.
func Installer(ctx context.Context, o Options) error {
	if o.ClePublique == "" {
		return ErrSansCle
	}
	cle, err := lireClePublique(o.ClePublique)
	if err != nil {
		return err
	}
	if !versionValide.MatchString(o.Version) {
		return fmt.Errorf("version annoncée par le serveur illisible : %q", o.Version)
	}
	if o.HTTP == nil {
		o.HTTP = http.DefaultClient
	}
	if o.BaseURL == "" {
		o.BaseURL = BaseParDefaut
	}
	base := strings.TrimRight(o.BaseURL, "/") + "/v" + o.Version + "/"

	// Le reste d'une mise à jour Windows précédente : l'exécutable d'alors ne
	// pouvait pas s'effacer lui-même.
	_ = os.Remove(o.Executable + ".old")

	sommes, err := lire(ctx, o.HTTP, base+"SHA256SUMS", tailleMaxSommes)
	if err != nil {
		return err
	}
	signature, err := lire(ctx, o.HTTP, base+"SHA256SUMS.minisig", tailleMaxSignature)
	if err != nil {
		return err
	}
	texte, err := verifier(cle, sommes, signature)
	if err != nil {
		return fmt.Errorf("release v%s refusée : %w", o.Version, err)
	}
	// Le commentaire de confiance est ce qui lie la signature à CETTE version :
	// sans lui, les sommes signées d'une release ancienne passeraient pour
	// celles de la release demandée.
	if attendu := "warden v" + o.Version; texte != attendu {
		return fmt.Errorf("release v%s refusée : %w (signée pour %q)", o.Version, ErrSignature, texte)
	}

	nom := NomDuBinaire(o.OS, o.Arch)
	attendue, err := sommeDe(sommes, nom)
	if err != nil {
		return fmt.Errorf("release v%s : %w", o.Version, err)
	}

	return remplacer(ctx, o, base+nom, attendue)
}

// remplacer écrit le binaire à côté de l'ancien, le vérifie, puis le renomme.
//
// À côté, et pas dans le répertoire temporaire : un renommage n'est atomique
// que dans un même système de fichiers.
func remplacer(ctx context.Context, o Options, url string, attendue []byte) error {
	dir := filepath.Dir(o.Executable)
	tmp, err := os.CreateTemp(dir, ".warden-*")
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return fmt.Errorf("%w : %s n'est pas inscriptible — réinstallez la CLI à la main, "+
				"ou dans un répertoire qui vous appartient (`make install`)", ErrNonInscriptible, dir)
		}
		return fmt.Errorf("%w : %w", ErrNonInscriptible, err)
	}
	// Retiré dans tous les cas où il n'a pas pris la place de l'ancien.
	defer func() { _ = os.Remove(tmp.Name()) }()

	h := sha256.New()
	err = copier(ctx, o.HTTP, url, io.MultiWriter(tmp, h), tailleMaxBinaire)
	if errFermeture := tmp.Close(); err == nil {
		err = errFermeture
	}
	if err != nil {
		return err
	}
	if obtenue := h.Sum(nil); !bytes.Equal(obtenue, attendue) {
		return fmt.Errorf("binaire refusé : sa somme ne correspond pas à la release signée")
	}

	if err := os.Chmod(tmp.Name(), 0o755); err != nil { //nolint:gosec // G302 : c'est un exécutable.
		return fmt.Errorf("binaire téléchargé : %w", err)
	}

	// Windows ne remplace pas un exécutable en cours : on l'écarte d'abord. Le
	// déplacer, lui, est permis.
	if o.OS == "windows" {
		if err := os.Rename(o.Executable, o.Executable+".old"); err != nil {
			return fmt.Errorf("%w : %w", ErrNonInscriptible, err)
		}
	}
	if err := os.Rename(tmp.Name(), o.Executable); err != nil {
		return fmt.Errorf("%w : %w", ErrNonInscriptible, err)
	}
	return nil
}

// sommeDe trouve la somme d'un fichier dans un `SHA256SUMS`.
func sommeDe(sommes []byte, nom string) ([]byte, error) {
	for ligne := range strings.SplitSeq(string(sommes), "\n") {
		champs := strings.Fields(ligne)
		// `sha256sum` préfixe d'un `*` le nom d'un fichier lu en binaire.
		if len(champs) == 2 && strings.TrimPrefix(champs[1], "*") == nom {
			somme, err := hex.DecodeString(champs[0])
			if err != nil || len(somme) != sha256.Size {
				return nil, fmt.Errorf("somme de %s illisible", nom)
			}
			return somme, nil
		}
	}
	return nil, fmt.Errorf("aucun binaire %s dans cette release", nom)
}

func lire(ctx context.Context, c *http.Client, url string, max int64) ([]byte, error) {
	var buf bytes.Buffer
	if err := copier(ctx, c, url, &buf, max); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// copier écrit le corps de `url` dans `w`, et refuse au-delà de `max` octets.
func copier(ctx context.Context, c *http.Client, url string, w io.Writer, max int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("téléchargement : %w", err)
	}
	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("téléchargement de %s : %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("téléchargement de %s : HTTP %d", url, resp.StatusCode)
	}
	n, err := io.Copy(w, io.LimitReader(resp.Body, max+1))
	if err != nil {
		return fmt.Errorf("téléchargement de %s : %w", url, err)
	}
	if n > max {
		return fmt.Errorf("téléchargement de %s : fichier trop volumineux", url)
	}
	return nil
}

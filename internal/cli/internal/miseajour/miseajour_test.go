package miseajour

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/crypto/blake2b"
)

// signataire tient une paire minisign de test.
type signataire struct {
	id   [8]byte
	priv ed25519.PrivateKey
	pub  string
}

func nouveauSignataire(t *testing.T) signataire {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var s signataire
	if _, err := rand.Read(s.id[:]); err != nil {
		t.Fatal(err)
	}
	s.priv = priv
	s.pub = base64.StdEncoding.EncodeToString(append(append([]byte("Ed"), s.id[:]...), pub...))
	return s
}

// signer produit ce que `minisign -S -t <texte>` produit : algorithme "ED",
// signature du BLAKE2b-512 du message.
func (s signataire) signer(message []byte, texte string) []byte {
	empreinte := blake2b.Sum512(message)
	sig := ed25519.Sign(s.priv, empreinte[:])
	globale := ed25519.Sign(s.priv, append(append([]byte{}, sig...), texte...))
	return []byte("untrusted comment: signature from minisign secret key\n" +
		base64.StdEncoding.EncodeToString(append(append([]byte("ED"), s.id[:]...), sig...)) + "\n" +
		"trusted comment: " + texte + "\n" +
		base64.StdEncoding.EncodeToString(globale) + "\n")
}

// release monte un faux hôte de releases servant une version.
type release struct {
	binaire, sommes, signature []byte

	// panne, si non nul, est le statut rendu à TOUTE requête.
	panne int
}

func (r release) servir(t *testing.T, version string) string {
	t.Helper()
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if r.panne != 0 {
			w.WriteHeader(r.panne)
			return
		}
		switch p := req.URL.Path; {
		case p == "/v"+version+"/SHA256SUMS":
			_, _ = w.Write(r.sommes)
		case p == "/v"+version+"/SHA256SUMS.minisig":
			_, _ = w.Write(r.signature)
		// Le même binaire sous le nom de toute cible : c'est `SHA256SUMS` qui
		// dit lesquelles la release contient.
		case strings.HasPrefix(p, "/v"+version+"/warden_"):
			_, _ = w.Write(r.binaire)
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(h.Close)
	return h.URL
}

func releaseSignee(s signataire, version string, binaire []byte) release {
	return releaseSigneePour(s, version, binaire, NomDuBinaire("linux", "amd64"))
}

// releaseSigneePour signe une release dont `SHA256SUMS` liste `binaire` sous
// `nom`, à côté d'une autre cible.
func releaseSigneePour(s signataire, version string, binaire []byte, nom string) release {
	somme := sha256.Sum256(binaire)
	sommes := []byte(hex.EncodeToString(somme[:]) + "  " + nom + "\n" +
		strings.Repeat("0", 64) + "  " + NomDuBinaire("darwin", "arm64") + "\n")
	return release{binaire: binaire, sommes: sommes, signature: s.signer(sommes, "warden v"+version)}
}

// installer pose un « ancien binaire » et lance la mise à jour contre `base`.
func installer(t *testing.T, base, cle, version string) (string, error) {
	t.Helper()
	return installerDans(t, t.TempDir(), base, cle, version, "linux")
}

func installerDans(t *testing.T, dir, base, cle, version, goos string) (string, error) {
	t.Helper()
	exe := filepath.Join(dir, "warden")
	if err := os.WriteFile(exe, []byte("ancien"), 0o755); err != nil { //nolint:gosec // exécutable de test
		t.Fatal(err)
	}
	err := Installer(t.Context(), Options{
		Version: version, BaseURL: base, ClePublique: cle,
		OS: goos, Arch: "amd64", Executable: exe,
	})
	return exe, err
}

func contenu(t *testing.T, chemin string) string {
	t.Helper()
	b, err := os.ReadFile(chemin) //nolint:gosec // chemin de test
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestUneReleaseSigneeRemplaceLeBinaire(t *testing.T) {
	s := nouveauSignataire(t)
	base := releaseSignee(s, "0.0.42", []byte("neuf")).servir(t, "0.0.42")

	exe, err := installer(t, base, s.pub, "0.0.42")
	if err != nil {
		t.Fatalf("Installer : %v", err)
	}
	if got := contenu(t, exe); got != "neuf" {
		t.Errorf("binaire = %q, attendu le neuf", got)
	}
	info, err := os.Stat(exe)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Errorf("binaire non exécutable : %v", info.Mode())
	}
}

// Chaque refus laisse l'ancien binaire INTACT : c'est ce qui rend la mise à
// jour sans risque à tenter.
func TestUneReleaseDouteuseEstRefuseeEtLAncienBinaireReste(t *testing.T) {
	s := nouveauSignataire(t)
	autre := nouveauSignataire(t)

	cas := []struct {
		nom     string
		release func() release
		cle     string
	}{
		{"binaire altéré", func() release {
			r := releaseSignee(s, "0.0.42", []byte("neuf"))
			r.binaire = []byte("piégé")
			return r
		}, s.pub},
		{"sommes altérées après signature", func() release {
			r := releaseSignee(s, "0.0.42", []byte("neuf"))
			piege := sha256.Sum256([]byte("piégé"))
			r.sommes = []byte(hex.EncodeToString(piege[:]) + "  " + NomDuBinaire("linux", "amd64") + "\n")
			r.binaire = []byte("piégé")
			return r
		}, s.pub},
		{"signée par une autre clé", func() release {
			return releaseSignee(autre, "0.0.42", []byte("neuf"))
		}, s.pub},
		// LE cas que le commentaire de confiance existe pour arrêter : une
		// release authentique, mais pas celle demandée.
		{"signée pour une autre version", func() release {
			r := releaseSignee(s, "0.0.42", []byte("ancien mais signé"))
			r.signature = s.signer(r.sommes, "warden v0.0.7")
			return r
		}, s.pub},
		{"hôte en erreur", func() release {
			return release{panne: http.StatusInternalServerError}
		}, s.pub},
		// Signées, pour que ce soit bien la BORNE qui refuse et non la signature.
		{"sommes au-delà de la borne", func() release {
			r := releaseSignee(s, "0.0.42", []byte("neuf"))
			r.sommes = append(r.sommes, strings.Repeat("#", tailleMaxSommes)...)
			r.signature = s.signer(r.sommes, "warden v0.0.42")
			return r
		}, s.pub},
		{"aucun binaire pour cette cible", func() release {
			return releaseSigneePour(s, "0.0.42", []byte("neuf"), NomDuBinaire("darwin", "amd64"))
		}, s.pub},
		{"commentaire de confiance réécrit", func() release {
			r := releaseSignee(s, "0.0.42", []byte("neuf"))
			r.signature = []byte(strings.Replace(string(s.signer(r.sommes, "warden v0.0.7")),
				"warden v0.0.7", "warden v0.0.42", 1))
			return r
		}, s.pub},
	}
	for _, c := range cas {
		t.Run(c.nom, func(t *testing.T) {
			base := c.release().servir(t, "0.0.42")
			exe, err := installer(t, base, c.cle, "0.0.42")
			if err == nil {
				t.Fatal("release acceptée")
			}
			if got := contenu(t, exe); got != "ancien" {
				t.Errorf("binaire = %q : l'ancien a été touché", got)
			}
		})
	}
}

func TestSansCleRienNEstTelecharge(t *testing.T) {
	appele := false
	h := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { appele = true }))
	t.Cleanup(h.Close)

	_, err := installer(t, h.URL, "", "0.0.42")
	if !errors.Is(err, ErrSansCle) {
		t.Errorf("err = %v, attendu ErrSansCle", err)
	}
	if appele {
		t.Error("une CLI sans clé a contacté l'hôte de téléchargement")
	}
}

// La version vient du serveur et finit dans une URL : elle est bornée.
func TestUneVersionIllisibleEstRefusee(t *testing.T) {
	s := nouveauSignataire(t)
	appele := false
	h := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { appele = true }))
	t.Cleanup(h.Close)

	for _, v := range []string{"../../evil", "0.0.1/x", "", "0.0.1?x=1"} {
		if _, err := installer(t, h.URL, s.pub, v); err == nil {
			t.Errorf("version %q acceptée", v)
		}
	}
	// Refusée AVANT la moindre requête : c'est la validation qui parle, pas
	// une erreur réseau qui passerait pour elle.
	if appele {
		t.Error("une version illisible a atteint l'hôte de téléchargement")
	}
}

// Le cas courant d'une installation système (`/usr/local/bin`) : refusé
// proprement, l'ancien binaire en place.
func TestUnRepertoireNonInscriptibleEstRefuse(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("les droits d'un répertoire ne s'imposent pas ici")
	}
	s := nouveauSignataire(t)
	base := releaseSignee(s, "0.0.42", []byte("neuf")).servir(t, "0.0.42")

	dir := t.TempDir()
	exe := filepath.Join(dir, "warden")
	if err := os.WriteFile(exe, []byte("ancien"), 0o755); err != nil { //nolint:gosec // exécutable de test
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil { //nolint:gosec // répertoire de test
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) }) //nolint:gosec // rendu à TempDir pour le nettoyage

	err := Installer(t.Context(), Options{
		Version: "0.0.42", BaseURL: base, ClePublique: s.pub,
		OS: "linux", Arch: "amd64", Executable: exe,
	})
	if !errors.Is(err, ErrNonInscriptible) {
		t.Errorf("err = %v, attendu ErrNonInscriptible", err)
	}
	if got := contenu(t, exe); got != "ancien" {
		t.Errorf("binaire = %q : l'ancien a été touché", got)
	}
}

// Le chemin Windows se joue sur n'importe quel système, puisque la cible est
// une option : l'ancien est écarté en `.old`, et le résidu d'une mise à jour
// précédente est retiré. Ce qui N'EST PAS testé ici est la raison d'être de ce
// chemin — le verrou que Windows pose sur un exécutable en cours.
func TestLeCheminWindowsEcarteLAncienBinaire(t *testing.T) {
	s := nouveauSignataire(t)
	base := releaseSigneePour(s, "0.0.42", []byte("neuf"), NomDuBinaire("windows", "amd64")).
		servir(t, "0.0.42")

	dir := t.TempDir()
	residu := filepath.Join(dir, "warden.old")
	if err := os.WriteFile(residu, []byte("résidu"), 0o600); err != nil {
		t.Fatal(err)
	}

	exe, err := installerDans(t, dir, base, s.pub, "0.0.42", "windows")
	if err != nil {
		t.Fatalf("Installer : %v", err)
	}
	if got := contenu(t, exe); got != "neuf" {
		t.Errorf("binaire = %q, attendu le neuf", got)
	}
	// Le résidu a été retiré, puis l'ancien a pris sa place.
	if got := contenu(t, residu); got != "ancien" {
		t.Errorf("warden.old = %q, attendu l'ancien binaire", got)
	}
}

func TestLeNomDuBinaireEstCeluiQueLaCIPublie(t *testing.T) {
	if got := NomDuBinaire("windows", "amd64"); got != "warden_windows_amd64.exe" {
		t.Errorf("windows : %q", got)
	}
	if got := NomDuBinaire("darwin", "arm64"); got != "warden_darwin_arm64" {
		t.Errorf("darwin : %q", got)
	}
}

// Un vecteur produit par le VRAI minisign (0.12, `minisign -G -W` puis
// `minisign -S -t "warden v0.0.42"`), et non par le signataire de test : c'est
// la seule chose qui dit que la vérification écrite ici lit le format réel, et
// pas seulement l'idée qu'on s'en fait.
func TestUnVecteurDuVraiMinisignVerifie(t *testing.T) {
	const (
		pub    = "RWSBjAIv6iuxS8Y1K/Kd4UmNc5vntvc9R9Pv7uItMOz4RmNXQViEeoAp"
		sommes = "abc  warden_linux_amd64\n"
		sig    = "untrusted comment: signature from minisign secret key\n" +
			"RUSBjAIv6iuxSyhSkXSkK79lMI5FXhxwlZOmZcUWFrQAnI/N93IZGgSWn/QNbJu0XCarcPlCspPevf7ETGeywPDGHh/+RRfUVAk=\n" +
			"trusted comment: warden v0.0.42\n" +
			"SGTe9pEvKKfaAMx7KcO1EEcYEgX3ew5UxCDxtBi9VEtoIkqbz+morEGNbUcQthcUjv4sDIPE0k3wKbhYZXl4Aw==\n"
	)
	cle, err := lireClePublique(pub)
	if err != nil {
		t.Fatalf("clé : %v", err)
	}
	texte, err := verifier(cle, []byte(sommes), []byte(sig))
	if err != nil {
		t.Fatalf("vérification : %v", err)
	}
	if texte != "warden v0.0.42" {
		t.Errorf("commentaire de confiance = %q", texte)
	}
	if _, err := verifier(cle, []byte(sommes+" "), []byte(sig)); err == nil {
		t.Error("un message modifié d'un octet est accepté")
	}
}

// La clé embarquée se lit. Une clé mal recopiée ne se verrait sinon qu'au
// premier `warden upgrade` d'un utilisateur — c'est-à-dire trop tard, puisque
// la CLI qui la porte est déjà distribuée.
//
// Une clé VIDE échoue aussi, et c'est voulu : `ErrSansCle` est le repli d'une
// CLI qu'on aurait compilée sans clé, pas un état qu'une release peut porter.
func TestLaCleEmbarqueeEstUneCleMinisign(t *testing.T) {
	if _, err := lireClePublique(ClePublique); err != nil {
		t.Fatalf("ClePublique : %v", err)
	}
}

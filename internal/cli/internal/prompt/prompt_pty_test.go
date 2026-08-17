//go:build linux

// L'extinction de l'écho est LA propriété de sécurité de ce paquet, et elle ne
// se vérifie que sur un vrai terminal : `term.IsTerminal` refuse tout le reste,
// et un `os.Pipe` n'a pas de discipline de ligne à éteindre.
//
// Le pseudo-terminal est ouvert à la main plutôt qu'avec une bibliothèque
// tierce : trois ioctl, aucune dépendance de plus dans un dépôt qui manipule des
// credentials. Linux uniquement, d'où le tag — les autres plateformes perdent ce
// test, pas le code.

package prompt_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"

	"github.com/jijins1/warden/internal/cli/internal/prompt"
)

// ouvrirPTY rend le couple maître / esclave d'un pseudo-terminal.
//
// Les ioctl passent par `SyscallConn().Control` et NON par `m.Fd()`. La
// distinction n'est pas cosmétique : `os.File.Fd()` sort le descripteur du
// scrutateur du runtime et le repasse en mode bloquant, ce qui fait cesser de
// fonctionner `SetReadDeadline` — et la lecture du maître, plus bas, ne rendait
// alors jamais la main. Mesuré : le test partait en boucle infinie.
func ouvrirPTY(t *testing.T) (maitre, esclave *os.File) {
	t.Helper()

	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("pas de pseudo-terminal disponible : %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	conn, err := m.SyscallConn()
	if err != nil {
		t.Skipf("SyscallConn : %v", err)
	}

	// Déverrouillage de l'esclave, puis son numéro. Sans le premier, l'ouverture
	// du second échoue avec EIO — erreur qui ne nomme pas la cause.
	var n int
	var ioctlErr error
	if err := conn.Control(func(fd uintptr) {
		if ioctlErr = unix.IoctlSetPointerInt(int(fd), unix.TIOCSPTLCK, 0); ioctlErr != nil {
			return
		}
		n, ioctlErr = unix.IoctlGetInt(int(fd), unix.TIOCGPTN)
	}); err != nil {
		t.Skipf("Control : %v", err)
	}
	if ioctlErr != nil {
		t.Skipf("ioctl du pseudo-terminal : %v", ioctlErr)
	}

	s, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("ouverture de l'esclave : %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if !term.IsTerminal(int(s.Fd())) {
		t.Fatal("l'esclave du pseudo-terminal n'est pas reconnu comme terminal")
	}
	return m, s
}

// lireDisponible vide ce que le maître a reçu.
func lireDisponible(t *testing.T, maitre *os.File) string {
	t.Helper()

	if err := maitre.SetReadDeadline(time.Now().Add(200 * time.Millisecond)); err != nil {
		t.Fatalf("échéance de lecture : %v", err)
	}
	var vu strings.Builder
	tampon := make([]byte, 4096)
	for {
		n, err := maitre.Read(tampon)
		vu.Write(tampon[:n])
		if err != nil {
			return vu.String()
		}
	}
}

// LE test. Un terminal dont l'écho est actif renvoie au maître chaque caractère
// frappé ; c'est cette réémission qui met un mot de passe dans un `script`, dans
// un enregistrement de session ou sous les yeux d'un voisin.
func TestLEchoEstCoupePendantLaSaisie(t *testing.T) {
	maitre, esclave := ouvrirPTY(t)

	// L'écho est ACTIF au départ : c'est l'état par défaut d'un terminal, et
	// sans cette vérification le test passerait aussi si Ask ne faisait rien.
	avant, err := unix.IoctlGetTermios(int(esclave.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatalf("termios : %v", err)
	}
	if avant.Lflag&unix.ECHO == 0 {
		t.Fatal("l'écho est déjà coupé avant l'appel : le test ne prouverait rien")
	}

	const secret = "hunter2-tres-secret"
	const invite = "mot de passe : "

	go func() {
		// Le temps que Ask coupe l'écho avant que la frappe arrive.
		time.Sleep(50 * time.Millisecond)
		_, _ = maitre.WriteString(secret + "\n")
	}()

	s, err := prompt.Ask(t.Context(), esclave, invite)
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if string(s.Reveal()) != secret {
		t.Fatalf("valeur lue = %q, attendu %q", s.Reveal(), secret)
	}

	vu := lireDisponible(t, maitre)
	if strings.Contains(vu, secret) {
		t.Errorf("le terminal a réémis la saisie : %q", vu)
	}
	// L'invite, elle, doit bien être arrivée — sinon l'utilisateur ne sait pas
	// ce qu'on lui demande, et le test ci-dessus passerait sur un terminal muet.
	if !strings.Contains(vu, invite) {
		t.Errorf("l'invite n'a pas été affichée : %q", vu)
	}
}

// Un terminal laissé sans écho est un shell inutilisable jusqu'à `stty sane`, ce
// que personne ne devine. `ReadPassword` restaure sur son propre retour, mais
// pas sur le chemin d'annulation — c'est celui-ci qu'on vérifie.
func TestLEchoEstRetabliApresUneAnnulation(t *testing.T) {
	maitre, esclave := ouvrirPTY(t)
	_ = maitre

	ctx, annuler := context.WithCancel(t.Context())
	go func() {
		time.Sleep(50 * time.Millisecond)
		annuler()
	}()

	_, err := prompt.Ask(ctx, esclave, "mot de passe : ")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("erreur = %v, attendu context.Canceled", err)
	}

	apres, err := unix.IoctlGetTermios(int(esclave.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatalf("termios : %v", err)
	}
	if apres.Lflag&unix.ECHO == 0 {
		t.Error("l'écho est resté coupé après annulation : le shell de l'utilisateur est muet")
	}
}

// Et sur le chemin nominal aussi, évidemment.
func TestLEchoEstRetabliApresUneSaisieReussie(t *testing.T) {
	maitre, esclave := ouvrirPTY(t)

	go func() {
		time.Sleep(50 * time.Millisecond)
		_, _ = maitre.WriteString("hunter2\n")
	}()

	if _, err := prompt.Ask(t.Context(), esclave, "mot de passe : "); err != nil {
		t.Fatalf("Ask: %v", err)
	}

	apres, err := unix.IoctlGetTermios(int(esclave.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatalf("termios : %v", err)
	}
	if apres.Lflag&unix.ECHO == 0 {
		t.Error("l'écho est resté coupé après une saisie réussie")
	}
}

// Une saisie vide est refusée : c'est presque toujours un Entrée malencontreux,
// et l'accepter produirait un échec d'authentification bien plus loin.
func TestUneSaisieVideEstRefusee(t *testing.T) {
	maitre, esclave := ouvrirPTY(t)

	go func() {
		time.Sleep(50 * time.Millisecond)
		_, _ = maitre.WriteString("\n")
	}()

	if _, err := prompt.Ask(t.Context(), esclave, "mot de passe : "); !errors.Is(err, prompt.ErrVide) {
		t.Fatalf("erreur = %v, attendu ErrVide", err)
	}
}

// Sonde. Elle ne teste PAS le paquet : elle teste le harnais.
//
// Sans elle, `TestLEchoEstCoupePendantLaSaisie` passerait aussi bien si le
// pseudo-terminal ne réémettait jamais rien — un test de sécurité qui réussit
// pour une mauvaise raison est pire qu'une absence de test. Celle-ci lit SANS
// couper l'écho et vérifie que le maître voit bien la frappe revenir.
func TestSondeLeHarnaisVoitBienLEcho(t *testing.T) {
	maitre, esclave := ouvrirPTY(t)

	const frappe = "hunter2-tres-secret"
	go func() {
		time.Sleep(50 * time.Millisecond)
		_, _ = maitre.WriteString(frappe + "\n")
	}()

	// Lecture ordinaire, écho intact.
	tampon := make([]byte, 256)
	n, err := esclave.Read(tampon)
	if err != nil {
		t.Fatalf("lecture de l'esclave : %v", err)
	}
	if !strings.Contains(string(tampon[:n]), frappe) {
		t.Fatalf("l'esclave n'a pas reçu la frappe : %q", tampon[:n])
	}

	if vu := lireDisponible(t, maitre); !strings.Contains(vu, frappe) {
		t.Errorf("le harnais ne voit pas l'écho : %q — les tests d'extinction "+
			"ci-dessus ne prouvent alors plus rien", vu)
	}
}

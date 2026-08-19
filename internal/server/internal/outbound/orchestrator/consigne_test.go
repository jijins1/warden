// Étage 1 de l'ADR-0010 : le clientset factice, aucun cluster.
//
// ATTENTION à ce que ce fichier ne peut PAS montrer : le clientset factice
// n'applique aucune validation de nom. C'est lui qui a laissé passer un nom de
// Secret en MAJUSCULES jusqu'au cluster réel, où l'API l'a refusé sur un message
// parlant de sous-domaines RFC 1123 sans jamais nommer le ULID. Le test
// ci-dessous vérifie donc la FORME du nom explicitement, puisque le harnais ne
// le fera pas à sa place.

package orchestrator_test

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/jijins1/warden/internal/server/internal/core"
	"github.com/jijins1/warden/internal/server/internal/outbound/orchestrator"
)

// nomRFC1123 est la forme qu'exige l'API Kubernetes pour un nom de Secret.
var nomRFC1123 = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)

func consigne(t *testing.T) (*orchestrator.Kubernetes, *fake.Clientset) {
	t.Helper()
	client := fake.NewSimpleClientset()
	k, err := orchestrator.New(client, orchestrator.Config{
		Namespace:   ns,
		DispatchURL: "http://d:8084",
		IngestURL:   "http://i:8085",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return k, client
}

// LE défaut trouvé par la chaîne réelle. Un ULID est en MAJUSCULES — l'ADR-0021
// le veut ainsi — et un nom de Secret doit être minuscule. Le clientset factice
// accepte les deux ; l'API réelle refuse.
func TestLeNomDuSecretDeConsigneEstUtilisableParKubernetes(t *testing.T) {
	k, client := consigne(t)

	const ulid = "01M0DWFQ0PDPWVWCQ1XVZY3M68" // majuscules, comme tout run_ref
	if err := k.Stash(t.Context(), ulid, []byte("hunter2")); err != nil {
		t.Fatalf("Stash: %v", err)
	}

	secrets, err := client.CoreV1().Secrets(ns).List(t.Context(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(secrets.Items) != 1 {
		t.Fatalf("%d Secrets créés, attendu 1", len(secrets.Items))
	}

	nom := secrets.Items[0].Name
	if !nomRFC1123.MatchString(nom) {
		t.Errorf("nom %q refusé par l'API Kubernetes : elle exige un label RFC 1123", nom)
	}
	if strings.ToLower(nom) != nom {
		t.Errorf("nom %q contient des majuscules", nom)
	}
}

// Stash et Take doivent composer le MÊME nom. Deux compositions jumelles qui
// divergent produiraient une valeur rangée et introuvable — donc un enrôlement
// qui échoue en disant que le mot de passe a déjà été consommé.
func TestStashEtTakeSAccordentSurLeNom(t *testing.T) {
	k, _ := consigne(t)

	const ulid = "01M0DWFQ0PDPWVWCQ1XVZY3M68"
	if err := k.Stash(t.Context(), ulid, []byte("hunter2")); err != nil {
		t.Fatalf("Stash: %v", err)
	}
	v, err := k.Take(t.Context(), ulid)
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	if string(v) != "hunter2" {
		t.Errorf("valeur = %q", v)
	}
}

// La lecture DÉTRUIT : une valeur qui ne sert qu'une fois et qu'on peut relire
// est une valeur qui traîne.
func TestLaLectureConsommeLaValeur(t *testing.T) {
	k, client := consigne(t)

	if err := k.Stash(t.Context(), "01M0DWFQ0PDPWVWCQ1XVZY3M68", []byte("hunter2")); err != nil {
		t.Fatalf("Stash: %v", err)
	}
	if _, err := k.Take(t.Context(), "01M0DWFQ0PDPWVWCQ1XVZY3M68"); err != nil {
		t.Fatalf("Take: %v", err)
	}

	if _, err := k.Take(t.Context(), "01M0DWFQ0PDPWVWCQ1XVZY3M68"); !errors.Is(err, core.ErrNoStash) {
		t.Errorf("erreur = %v, attendu core.ErrNoStash — la valeur a survécu à sa lecture", err)
	}
	secrets, _ := client.CoreV1().Secrets(ns).List(t.Context(), metav1.ListOptions{})
	if len(secrets.Items) != 0 {
		t.Errorf("%d Secrets restants après consommation", len(secrets.Items))
	}
}

// Reprendre un enrôlement interrompu doit REMPLACER le mot de passe précédent,
// pas échouer sur sa présence.
func TestReposerUneValeurLaRemplace(t *testing.T) {
	k, _ := consigne(t)

	const ulid = "01M0DWFQ0PDPWVWCQ1XVZY3M68"
	_ = k.Stash(t.Context(), ulid, []byte("ancien"))
	if err := k.Stash(t.Context(), ulid, []byte("nouveau")); err != nil {
		t.Fatalf("Stash (reprise): %v", err)
	}
	v, err := k.Take(t.Context(), ulid)
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	if string(v) != "nouveau" {
		t.Errorf("valeur = %q, attendu la plus récente", v)
	}
}

func TestUneConsigneVideOuSansCleEstRefusee(t *testing.T) {
	k, _ := consigne(t)

	if err := k.Stash(t.Context(), "", []byte("x")); err == nil {
		t.Error("une clé vide a été acceptée")
	}
	if err := k.Stash(t.Context(), "abc", nil); err == nil {
		t.Error("une valeur vide a été acceptée")
	}
	if _, err := k.Take(t.Context(), ""); err == nil {
		t.Error("une lecture sans clé a été acceptée")
	}
}

// Une valeur jamais consignée le dit clairement : le composeur d'ordres
// distingue ce cas d'une panne, et livre l'ordre sans mot de passe.
func TestUneValeurAbsenteEstDistinguee(t *testing.T) {
	k, _ := consigne(t)

	if _, err := k.Take(t.Context(), "01M0DWFQ0PDPWVWCQ1XVZY3M68"); !errors.Is(err, core.ErrNoStash) {
		t.Errorf("erreur = %v, attendu core.ErrNoStash", err)
	}
}

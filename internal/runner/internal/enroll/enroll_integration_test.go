//go:build integration

// Étage 2 de l'ADR-0010 : un VRAI sshd, dans un conteneur jetable.
//
// L'étage 1 monte un serveur SSH écrit à la main. Il vérifie ce que le CODE
// fait — l'épinglage, l'idempotence, l'annulation — et ne peut rien dire de ce
// qu'une VRAIE machine fait : les permissions qu'un sshd exige de
// `~/.ssh/authorized_keys` avant d'accepter une clé, le comportement du shell
// distant, l'authentification par clé publique réelle.
//
// C'est cette moitié-là que ce fichier couvre, et elle n'est pas décorative :
// un `authorized_keys` en 0644 fait refuser la clé par sshd, en silence côté
// client. Le test d'étage 1 ne pouvait pas le voir.
//
// La cible est délibérément NUE — ni Python, ni sudo — parce que c'est le cas
// qu'un enrôlement doit couvrir : une machine fraîchement installée.

package enroll_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/jijins1/warden/internal/runner/internal/enroll"
)

// demarrerCible construit et lance une machine à enrôler.
func demarrerCible(t *testing.T) (adresse string, port int) {
	t.Helper()
	ctx := context.Background()

	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			FromDockerfile: testcontainers.FromDockerfile{
				Context: "testdata/cible",
			},
			ExposedPorts: []string{"22/tcp"},
			// Sur le message de sshd et non sur le port ouvert : le port est
			// lié avant que le démon accepte, et s'y connecter trop tôt donne un
			// « connection reset » qui ressemble à un défaut du produit.
			WaitingFor: wait.ForLog("Server listening on 0.0.0.0 port 22"),
		},
		Started: true,
	})
	if err != nil {
		t.Skipf("cible d'enrôlement indisponible : %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(c) })

	hote, err := c.Host(ctx)
	if err != nil {
		t.Fatalf("hôte du conteneur : %v", err)
	}
	mappe, err := c.MappedPort(ctx, "22/tcp")
	if err != nil {
		t.Fatalf("port du conteneur : %v", err)
	}
	n, err := strconv.Atoi(mappe.Port())
	if err != nil {
		t.Fatalf("port %q illisible : %v", mappe.Port(), err)
	}
	return hote, n
}

func cibleReelle(t *testing.T, adresse string, port int) enroll.Cible {
	t.Helper()
	return enroll.Cible{
		Host: "cible-de-test", Address: adresse, Port: port,
		Login: "ansible", Password: []byte("hunter2"),
		PublicKey: clePublique(t),
	}
}

// Le chemin nominal contre un vrai sshd, du premier contact à la vérification.
func TestUnEnrolementReelPoseLaCleEtSeVerifie(t *testing.T) {
	adresse, port := demarrerCible(t)
	c := cibleReelle(t, adresse, port)

	var etapes []string
	res, err := enroll.Enroler(t.Context(), c, func(e string) { etapes = append(etapes, e) })
	if err != nil {
		t.Fatalf("Enroler: %v\nrécit :\n  %s", err, strings.Join(etapes, "\n  "))
	}

	if !strings.HasPrefix(res.Fingerprint, "SHA256:") {
		t.Errorf("empreinte = %q", res.Fingerprint)
	}
	if res.DejaPresente {
		t.Error("la clé est signalée déjà présente sur une machine vierge")
	}
	// La VÉRIFICATION finale a réussi : c'est elle qui distingue « on a écrit un
	// fichier » de « la machine est joignable ». Contre un vrai sshd, elle met
	// en jeu les permissions du fichier — un 0644 le ferait refuser en silence.
	if !strings.Contains(strings.Join(etapes, "\n"), "vérifié") {
		t.Errorf("l'enrôlement ne s'est pas vérifié :\n  %s", strings.Join(etapes, "\n  "))
	}
}

// L'idempotence contre un vrai `grep -qxF` dans un vrai shell.
func TestUnSecondEnrolementReelNeDoublePasLaLigne(t *testing.T) {
	adresse, port := demarrerCible(t)
	c := cibleReelle(t, adresse, port)

	premier, err := enroll.Enroler(t.Context(), c, nil)
	if err != nil {
		t.Fatalf("premier enrôlement : %v", err)
	}
	second, err := enroll.Enroler(t.Context(), c, nil)
	if err != nil {
		t.Fatalf("second enrôlement : %v", err)
	}

	if !second.DejaPresente {
		t.Error("le second enrôlement n'a pas vu la clé déjà posée")
	}
	// L'empreinte ne bouge pas : la clé d'hôte est frappée au démarrage du
	// conteneur, donc stable pour sa durée de vie.
	if second.HostKey != premier.HostKey {
		t.Error("l'empreinte a changé entre deux enrôlements de la même machine")
	}
}

// L'ÉPINGLAGE contre deux vraies machines. Chaque conteneur frappe sa propre clé
// d'hôte au démarrage : la substitution est donc réelle, pas simulée.
func TestUneMachineSubstitueeEstRefusee(t *testing.T) {
	adresse, port := demarrerCible(t)
	_, portAutre := demarrerCible(t)

	// L'empreinte de l'AUTRE machine, obtenue en l'enrôlant d'abord.
	autre, err := enroll.Enroler(t.Context(), cibleReelle(t, adresse, portAutre), nil)
	if err != nil {
		t.Fatalf("enrôlement de la seconde machine : %v", err)
	}

	c := cibleReelle(t, adresse, port)
	c.HostKey = autre.HostKey

	if _, err := enroll.Enroler(t.Context(), c, nil); !errors.Is(err, enroll.ErrEmpreinteChangee) {
		t.Fatalf("erreur = %v, attendu ErrEmpreinteChangee", err)
	}
}

// Une empreinte déclarée QUI CORRESPOND laisse passer : sans ce cas, le test
// ci-dessus passerait aussi si l'épinglage refusait tout.
func TestUneEmpreinteCorrecteLaissePasser(t *testing.T) {
	adresse, port := demarrerCible(t)
	c := cibleReelle(t, adresse, port)

	premier, err := enroll.Enroler(t.Context(), c, nil)
	if err != nil {
		t.Fatalf("premier enrôlement : %v", err)
	}

	c.HostKey = premier.HostKey
	if _, err := enroll.Enroler(t.Context(), c, nil); err != nil {
		t.Fatalf("un enrôlement épinglé sur la bonne empreinte a échoué : %v", err)
	}
}

func TestUnMotDePasseRefuseParUnVraiSshd(t *testing.T) {
	adresse, port := demarrerCible(t)
	c := cibleReelle(t, adresse, port)
	c.Password = []byte("ce-n-est-pas-le-bon")

	if _, err := enroll.Enroler(t.Context(), c, nil); !errors.Is(err, enroll.ErrAuthentification) {
		t.Fatalf("erreur = %v, attendu ErrAuthentification", err)
	}
}

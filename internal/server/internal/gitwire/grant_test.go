package gitwire_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jijins1/warden/internal/server/internal/gitwire"
)

const secretDeTest = "secret-interne-partage-de-test"

func porteeValide() gitwire.PushGrant {
	return gitwire.PushGrant{
		Repo:     "depot",
		Ref:      "refs/audit/alice/01JZQ0000000000000000ALIC",
		Mainline: "refs/heads/main",
		Expires:  time.Now().Add(time.Hour).Unix(),
	}
}

// TestAllerRetour : ce que `api` frappe, `gitd` le relit à l'identique.
func TestAllerRetour(t *testing.T) {
	jeton, err := gitwire.SignPushGrant(porteeValide(), secretDeTest)
	if err != nil {
		t.Fatalf("SignPushGrant : %v", err)
	}

	relu, err := gitwire.ParsePushGrant(jeton, secretDeTest)
	if err != nil {
		t.Fatalf("ParsePushGrant : %v", err)
	}
	if relu.Repo != "depot" || relu.Ref != porteeValide().Ref || relu.Mainline != "refs/heads/main" {
		t.Errorf("portée altérée à l'aller-retour : %+v", relu)
	}
}

// TestPorteeAlterreeEstRefusee est la propriété qui rend ce format tenable.
//
// Le jeton voyage par le poste de l'utilisateur : il est intégralement sous le
// contrôle de qui le présente. Ce n'est pas un problème tant que le modifier le
// rend invalide — c'est précisément ce que la signature achète, et c'est
// l'ancrage du contrôle de fraîcheur qui en dépend le plus.
func TestPorteeAlterreeEstRefusee(t *testing.T) {
	jeton, err := gitwire.SignPushGrant(porteeValide(), secretDeTest)
	if err != nil {
		t.Fatalf("SignPushGrant : %v", err)
	}

	version, reste, _ := strings.Cut(jeton, ".")
	charge, sig, _ := strings.Cut(reste, ".")

	cas := []struct {
		nom    string
		altere string
	}{
		{"charge remplacée", version + ".YWJj." + sig},
		{"signature tronquée", version + "." + charge + "." + sig[:len(sig)-4]},
		{"signature d'un autre jeton", version + "." + charge + ".AAAA"},
		{"version inconnue", "wpg2." + charge + "." + sig},
		{"séparateurs manquants", charge},
		{"jeton vide", ""},
	}

	for _, c := range cas {
		t.Run(c.nom, func(t *testing.T) {
			if _, err := gitwire.ParsePushGrant(c.altere, secretDeTest); err == nil {
				t.Fatal("un jeton altéré a été accepté")
			}
		})
	}
}

// TestUnAutreSecretNeValidePas : la signature est ce qui distingue le rôle `api`
// de n'importe qui d'autre.
func TestUnAutreSecretNeValidePas(t *testing.T) {
	jeton, err := gitwire.SignPushGrant(porteeValide(), secretDeTest)
	if err != nil {
		t.Fatalf("SignPushGrant : %v", err)
	}
	if _, err := gitwire.ParsePushGrant(jeton, "un-autre-secret"); err == nil {
		t.Fatal("un jeton signé par un autre secret a été accepté")
	}
}

// TestJetonExpireEstRefuse : sans révocation, l'expiration est la seule borne de
// vie du jeton. Elle doit donc être vérifiée, et pas seulement transportée.
func TestJetonExpireEstRefuse(t *testing.T) {
	portee := porteeValide()
	portee.Expires = time.Now().Add(-time.Second).Unix()

	jeton, err := gitwire.SignPushGrant(portee, secretDeTest)
	if err != nil {
		t.Fatalf("SignPushGrant : %v", err)
	}
	if _, err := gitwire.ParsePushGrant(jeton, secretDeTest); err == nil {
		t.Fatal("un jeton expiré a été accepté")
	}
}

// TestSignatureRefuseUnePorteeIncomplete : échouer à la frappe plutôt qu'à la
// vérification. Un jeton sans ref serait refusé bien plus tard, du côté où
// personne ne peut le corriger.
func TestSignatureRefuseUnePorteeIncomplete(t *testing.T) {
	cas := []struct {
		nom    string
		portee gitwire.PushGrant
	}{
		{"sans dépôt", gitwire.PushGrant{Ref: "refs/audit/a/b", Expires: 1 << 40}},
		{"sans ref", gitwire.PushGrant{Repo: "depot", Expires: 1 << 40}},
		{"sans expiration", gitwire.PushGrant{Repo: "depot", Ref: "refs/audit/a/b"}},
	}
	for _, c := range cas {
		t.Run(c.nom, func(t *testing.T) {
			if _, err := gitwire.SignPushGrant(c.portee, secretDeTest); err == nil {
				t.Fatal("une portée incomplète a été signée")
			}
		})
	}

	if _, err := gitwire.SignPushGrant(porteeValide(), ""); err == nil {
		t.Fatal("un jeton a été signé sans secret : il serait forgeable")
	}
}

// TestLEchecNApprendRien : un seul motif d'erreur, quelle que soit la cause.
//
// `gitd` est joignable depuis du domaine hostile. Distinguer « signature
// fausse » d'« expiré » dirait au sondeur laquelle de ses deux hypothèses est la
// bonne — c'est la posture que le middleware de l'API interne tient déjà.
func TestLEchecNApprendRien(t *testing.T) {
	expire := porteeValide()
	expire.Expires = time.Now().Add(-time.Hour).Unix()
	jetonExpire, _ := gitwire.SignPushGrant(expire, secretDeTest)
	jetonValide, _ := gitwire.SignPushGrant(porteeValide(), secretDeTest)

	for _, jeton := range []string{jetonExpire, jetonValide + "x", "n'importe quoi"} {
		_, err := gitwire.ParsePushGrant(jeton, secretDeTest)
		if !errors.Is(err, gitwire.ErrGrantInvalid) {
			t.Errorf("erreur distinguable pour %.20q : %v", jeton, err)
		}
	}
}

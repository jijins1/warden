// Étage 1 de l'ADR-0010 : ni base, ni cluster.
//
// Un contrôle d'autorisation se juge sur ses REFUS, et surtout sur ses refus
// quand quelque chose va mal : magasin absent, lecture en panne, environnement
// vide. Un contrôle qui laisse passer dans ces cas-là n'est pas un contrôle.

package core_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jijins1/warden/internal/server/internal/core"
)

// droitsFactices double le magasin des droits.
type droitsFactices struct {
	// accordes indexe les paires autorisées, par "userID/environnement".
	accordes map[string]struct{}
	err      error

	// demandes compte les lectures, pour vérifier que le contrôle a bien lieu.
	demandes int
}

func nouveauxDroits(paires ...string) *droitsFactices {
	d := &droitsFactices{accordes: map[string]struct{}{}}
	for _, p := range paires {
		d.accordes[p] = struct{}{}
	}
	return d
}

func (d *droitsFactices) PeutAtteindre(_ context.Context, userID int64, env string) (bool, error) {
	d.demandes++
	if d.err != nil {
		return false, d.err
	}
	_, ok := d.accordes[cle(userID, env)]
	return ok, nil
}

func cle(userID int64, env string) string {
	return strconv.FormatInt(userID, 10) + "/" + env
}

func (d *droitsFactices) ListGrants(context.Context, string) ([]core.EnvironmentGrant, error) {
	return nil, nil
}

func (d *droitsFactices) Grant(context.Context, string, string, int64) (core.EnvironmentGrant, error) {
	return core.EnvironmentGrant{}, nil
}

func (d *droitsFactices) Revoke(context.Context, string, string) error { return nil }

// --- ce que le contrôle laisse passer ---

func TestUnDroitAccordeOuvreLEnvironnement(t *testing.T) {
	t.Parallel()

	d := nouveauxDroits(cle(7, "prod"))
	if err := core.VerifierLAcces(t.Context(), d, 7, "prod"); err != nil {
		t.Errorf("un droit accordé a été refusé : %v", err)
	}
}

// --- ce qu'il refuse ---

func TestSansDroitLEnvironnementEstInterdit(t *testing.T) {
	t.Parallel()

	d := nouveauxDroits(cle(7, "staging"))

	err := core.VerifierLAcces(t.Context(), d, 7, "prod")
	if !errors.Is(err, core.ErrEnvironnementInterdit) {
		t.Errorf("err = %v, attendu ErrEnvironnementInterdit", err)
	}
}

// Le droit est porté par le COUPLE. Un droit sur `prod` pour quelqu'un d'autre ne
// vaut rien pour moi — la vérification lit bien l'utilisateur.
func TestUnDroitNeSePreteEntreComptes(t *testing.T) {
	t.Parallel()

	d := nouveauxDroits(cle(8, "prod"))

	if err := core.VerifierLAcces(t.Context(), d, 7, "prod"); !errors.Is(err, core.ErrEnvironnementInterdit) {
		t.Errorf("err = %v : le droit d'un autre compte a servi", err)
	}
}

// LE test qui compte. Un magasin de droits absent doit INTERDIRE, pas laisser
// passer : un contrôle d'autorisation qui s'efface quand sa dépendance manque
// n'est pas un contrôle, et un déploiement mal assemblé doit se voir tout de
// suite plutôt que de tourner ouvert pendant des mois.
func TestUnMagasinAbsentInterditTout(t *testing.T) {
	t.Parallel()

	err := core.VerifierLAcces(t.Context(), nil, 7, "prod")
	if !errors.Is(err, core.ErrEnvironnementInterdit) {
		t.Fatalf("err = %v : sans magasin de droits, l'accès a été accordé", err)
	}
}

// Une panne de LECTURE ne donne pas l'accès non plus. C'est la différence entre
// un contrôle et une formalité — et le mode de panne le plus probable, puisque
// la base peut être indisponible un instant.
func TestUnePanneDeLectureNeDonnePasLAcces(t *testing.T) {
	t.Parallel()

	d := nouveauxDroits(cle(7, "prod"))
	d.err = errors.New("base indisponible")

	err := core.VerifierLAcces(t.Context(), d, 7, "prod")
	if err == nil {
		t.Fatal("une panne de lecture a laissé passer")
	}
	// Ce n'est PAS un refus de droit : l'exploitant doit pouvoir distinguer
	// « pas autorisé » de « on n'a pas pu savoir ».
	if errors.Is(err, core.ErrEnvironnementInterdit) {
		t.Errorf("err = %v : une panne s'est présentée comme un refus de droit", err)
	}
}

// Un environnement VIDE passe, et ce n'est pas un trou : ce contrôle garde la
// CLÉ D'ACCÈS, et un run sans environnement n'en reçoit aucune. Sans credential,
// le pod ne joint aucune machine.
//
// Le refuser casserait au contraire les playbooks en `connection: local`, qui
// n'ont jamais eu besoin d'environnement — et qui sont le cas de tous les runs du
// produit jusqu'ici.
func TestUnEnvironnementVideNaRienAAutoriser(t *testing.T) {
	t.Parallel()

	d := nouveauxDroits()

	if err := core.VerifierLAcces(t.Context(), d, 7, ""); err != nil {
		t.Errorf("un run sans environnement a été refusé : %v", err)
	}
	if d.demandes != 0 {
		t.Error("le magasin a été interrogé pour un environnement vide")
	}

	// Et même sans magasin monté : il n'y a rien à demander.
	if err := core.VerifierLAcces(t.Context(), nil, 7, ""); err != nil {
		t.Errorf("err = %v", err)
	}
}

// Le message nomme l'environnement refusé : sans lui, l'utilisateur ne sait pas
// quel accès demander, et ira chercher une faute de frappe.
func TestLeRefusNommeLEnvironnement(t *testing.T) {
	t.Parallel()

	err := core.VerifierLAcces(t.Context(), nouveauxDroits(), 7, "prod")
	if err == nil || !strings.Contains(err.Error(), "prod") {
		t.Errorf("le message ne nomme pas l'environnement : %v", err)
	}
}

// Un droit révoqué n'est plus un droit. La règle vit dans le magasin — la
// condition est dans le WHERE — et ce test dit que le cœur ne la contourne pas
// en interprétant lui-même un descripteur.
func TestUnDroitRevoqueNeVautPlusRien(t *testing.T) {
	t.Parallel()

	d := nouveauxDroits(cle(7, "prod"))
	if err := core.VerifierLAcces(t.Context(), d, 7, "prod"); err != nil {
		t.Fatalf("droit accordé refusé : %v", err)
	}

	delete(d.accordes, cle(7, "prod")) // ce que fait une révocation, vu d'ici

	if err := core.VerifierLAcces(t.Context(), d, 7, "prod"); !errors.Is(err, core.ErrEnvironnementInterdit) {
		t.Errorf("err = %v : un droit révoqué vaut encore", err)
	}
}

func TestRevokedDitCeQuIlDit(t *testing.T) {
	t.Parallel()

	var vivant core.EnvironmentGrant
	if vivant.Revoked() {
		t.Error("un droit sans date de révocation se dit révoqué")
	}

	eteint := core.EnvironmentGrant{RevokedAt: time.Now()}
	if !eteint.Revoked() {
		t.Error("un droit daté ne se dit pas révoqué")
	}
}

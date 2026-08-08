// Étage 1 de l'ADR-0010 : ni base, ni conteneur, ni cluster.
// Si ce fichier se met à en avoir besoin, c'est qu'un test a glissé d'étage.

package gitwire_test

import (
	"encoding/json"
	"testing"

	"github.com/jijins1/warden/internal/server/internal/gitwire"
)

// TestNomsDuContratFiges verrouille les noms de champs JSON.
//
// Les deux extrémités de l'arête sont déployées séparément (ADR-0006) : un
// renommage doit faire échouer un test, pas un déploiement.
func TestNomsDuContratFiges(t *testing.T) {
	encode, err := json.Marshal(gitwire.DiffRequest{
		Repo: "warden", From: "aaaa", To: "bbbb",
	})
	if err != nil {
		t.Fatalf("encodage: %v", err)
	}
	const attendu = `{"repo":"warden","from":"aaaa","to":"bbbb"}`
	if string(encode) != attendu {
		t.Errorf("forme de DiffRequest = %s, attendu %s", encode, attendu)
	}

	encode, err = json.Marshal(gitwire.Error{Code: gitwire.CodeUnknownRepo, Message: "x"})
	if err != nil {
		t.Fatalf("encodage: %v", err)
	}
	const attenduErr = `{"code":"unknown_repo","message":"x"}`
	if string(encode) != attenduErr {
		t.Errorf("forme d'Error = %s, attendu %s", encode, attenduErr)
	}
}

// TestStatutsDesCodes fige la correspondance code → statut.
func TestStatutsDesCodes(t *testing.T) {
	cas := []struct {
		code   gitwire.Code
		statut int
	}{
		{gitwire.CodeBadRequest, 400},
		{gitwire.CodeUnknownRepo, 404},
		{gitwire.CodeUnknownObject, 404},
		{gitwire.CodeInternal, 500},
	}
	for _, c := range cas {
		if got := c.code.HTTPStatus(); got != c.statut {
			t.Errorf("%s.HTTPStatus() = %d, attendu %d", c.code, got, c.statut)
		}
	}

	// Un code inconnu ne doit pas se traduire en succès : le repli est 500.
	if got := gitwire.Code("inventé").HTTPStatus(); got != 500 {
		t.Errorf("code inconnu = %d, attendu 500", got)
	}
}

// TestReplideStatut vérifie le chemin dégradé, quand le corps n'est pas
// décodable et que le client n'a que le statut.
func TestRepliDeStatut(t *testing.T) {
	if got := gitwire.CodeFromStatus(400); got != gitwire.CodeBadRequest {
		t.Errorf("400 -> %s", got)
	}
	// 404 ne peut pas se départager sans le corps : c'est la raison d'être du
	// champ Code, et le repli choisit le cas que l'audit doit traiter.
	if got := gitwire.CodeFromStatus(404); got != gitwire.CodeUnknownObject {
		t.Errorf("404 -> %s", got)
	}
	if got := gitwire.CodeFromStatus(503); got != gitwire.CodeInternal {
		t.Errorf("503 -> %s", got)
	}
}

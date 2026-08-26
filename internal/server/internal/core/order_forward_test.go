// Étage 1 de l'ADR-0010 : ni Docker, ni cluster, ni base.
//
// Séparé de `composition_test.go` : ces tests-ci portent sur la LIVRAISON de
// l'ordre — la consigne consommée, les valeurs qui montent dans `wire.Order` —
// et non sur la composition de la configuration.
package core_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jijins1/warden/internal/server/internal/core"
)

// consigneOrdre rend ce qu'on lui a rangé, et note ce qu'on lui a repris.
type consigneOrdre struct {
	valeurs  map[string][]byte
	reprises []string
}

func (c *consigneOrdre) Stash(_ context.Context, cle string, v []byte) error {
	if c.valeurs == nil {
		c.valeurs = map[string][]byte{}
	}
	c.valeurs[cle] = v
	return nil
}

func (c *consigneOrdre) Take(_ context.Context, cle string) ([]byte, error) {
	c.reprises = append(c.reprises, cle)
	v, ok := c.valeurs[cle]
	if !ok {
		return nil, core.ErrNoStash
	}
	delete(c.valeurs, cle)
	return v, nil
}

// L'ordre porte les variables, et la consigne est CONSOMMÉE au passage : c'est
// ce qui garantit qu'aucune valeur ne survit à la livraison (ADR-0018).
func TestOrdrePorteLesVariablesEtConsommeLaConsigne(t *testing.T) {
	run := runValide()
	consigne := &consigneOrdre{}
	_ = consigne.Stash(t.Context(), core.CleForwardEnv(run.RunRef),
		[]byte(`{"VAULT_ADDR":"https://vault"}`))

	b, err := core.NewOrderBuilder(runsFactices{1: run}, &frappeurFactice{}, nil, "https://git.invalid")
	if err != nil {
		t.Fatalf("NewOrderBuilder : %v", err)
	}
	b.AvecEnrolement(nil, nil, consigne)

	ordre, err := b.OrderFor(t.Context(), 1)
	if err != nil {
		t.Fatalf("OrderFor : %v", err)
	}
	if string(ordre.ForwardEnv["VAULT_ADDR"].Reveal()) != "https://vault" {
		t.Errorf("VAULT_ADDR absente ou fausse : %v", core.NomsTransmis(nil))
	}

	// Rejouer la livraison ne retrouve rien : la lecture a détruit la valeur.
	ordre2, err := b.OrderFor(t.Context(), 1)
	if err != nil {
		t.Fatalf("seconde OrderFor : %v", err)
	}
	if len(ordre2.ForwardEnv) != 0 {
		t.Error("la consigne doit être consommée par sa lecture")
	}
}

// Une consigne illisible fait échouer la livraison SANS citer son contenu.
func TestOrdreRefuseUneConsigneIllisible(t *testing.T) {
	run := runValide()
	consigne := &consigneOrdre{}
	_ = consigne.Stash(t.Context(), core.CleForwardEnv(run.RunRef), []byte("pas du json"))

	b, _ := core.NewOrderBuilder(runsFactices{1: run}, &frappeurFactice{}, nil, "https://git.invalid")
	b.AvecEnrolement(nil, nil, consigne)

	_, err := b.OrderFor(t.Context(), 1)
	if err == nil {
		t.Fatal("une consigne illisible doit faire échouer la livraison")
	}
	if strings.Contains(err.Error(), "pas du json") {
		t.Errorf("le message ne doit jamais citer le contenu de la consigne : %v", err)
	}
}

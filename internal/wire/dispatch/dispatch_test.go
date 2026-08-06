// Étage 1 de l'ADR-0010 : ni base, ni conteneur, ni cluster.
//
// Ce fichier tient la dette que l'ADR-0018 laissait explicitement ouverte —
// « l'encodage des secrets dans le flux […] doit être fixé explicitement, pas
// hérité d'un défaut de bibliothèque » — et l'interdiction de journaliser le
// corps des messages, qui n'est une garantie que si elle est vérifiée.
package dispatch_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/jijins1/warden/internal/wire/dispatch"
)

// Une vraie clé privée est multi-lignes. Écrite telle quelle dans une trame
// SSE, elle ne serait pas « corrompue » : sa première ligne vide terminerait le
// message, et le reste serait lu comme un message suivant.
const clePEM = `-----BEGIN OPENSSH PRIVATE KEY-----
b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gt
ZWQyNTUxOQAAACDpUneOK1ZQ+hOSJ0mMRUvFxk6nJxqRJvXcuQ8f2z0kBw

-----END OPENSSH PRIVATE KEY-----
`

func ordre() dispatch.Order {
	return dispatch.Order{
		RunRef:      "01JZQ00000000000000000ABCD",
		AuditSHA:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Environment: "staging",
		Inventory:   "inv/staging",
		Playbook:    "site.yml",
		Flags:       []string{"--check", "--diff"},
		GitURL:      "https://warden.internal/git/infra",
		GitToken:    dispatch.Secret("ght_0123456789abcdef"),
		SSHKey:      dispatch.Secret(clePEM),
		ForwardEnv: map[string]dispatch.Secret{
			"VAULT_DB_PASSWORD": dispatch.Secret("s3cr3t\navec un saut"),
		},
	}
}

func TestAllerRetourDUnOrdre(t *testing.T) {
	var buf bytes.Buffer
	envoye := ordre()
	if err := dispatch.Encode(&buf, dispatch.Message{Kind: dispatch.KindOrder, Order: &envoye}); err != nil {
		t.Fatalf("Encode: %v", err)
	}

	m, err := dispatch.NewDecoder(&buf).Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if m.Kind != dispatch.KindOrder {
		t.Fatalf("type = %q", m.Kind)
	}
	if m.Order == nil {
		t.Fatal("ordre vide")
	}

	recu := *m.Order
	if recu.RunRef != envoye.RunRef || recu.AuditSHA != envoye.AuditSHA ||
		recu.Playbook != envoye.Playbook || len(recu.Flags) != len(envoye.Flags) {
		t.Errorf("enveloppe altérée: %+v", recu)
	}
	// Le point qui compte : la clé privée traverse le canal OCTET POUR OCTET,
	// sauts de ligne compris.
	if string(recu.SSHKey.Reveal()) != clePEM {
		t.Errorf("la clé privée n'a pas survécu au transport:\n%q", recu.SSHKey.Reveal())
	}
	if string(recu.GitToken.Reveal()) != string(envoye.GitToken.Reveal()) {
		t.Error("le jeton git n'a pas survécu au transport")
	}
	if v := recu.ForwardEnv["VAULT_DB_PASSWORD"]; string(v.Reveal()) != "s3cr3t\navec un saut" {
		t.Errorf("la variable transmise n'a pas survécu: %q", v.Reveal())
	}
}

// La dette de l'ADR-0018, prise au mot : une trame contient exactement une
// ligne `data:`, et aucun saut de ligne issu d'une valeur.
func TestUneSeuleLigneDeDonneesParTrame(t *testing.T) {
	var buf bytes.Buffer
	o := ordre()
	if err := dispatch.Encode(&buf, dispatch.Message{Kind: dispatch.KindOrder, Order: &o}); err != nil {
		t.Fatalf("Encode: %v", err)
	}

	trame := buf.String()
	if n := strings.Count(trame, "data:"); n != 1 {
		t.Errorf("%d lignes `data:` dans la trame, attendu 1", n)
	}
	if !strings.HasSuffix(trame, "\n\n") {
		t.Error("la trame ne se termine pas par une ligne vide")
	}
	// Trois lignes utiles : `event:`, `data:`, et la ligne vide de fin.
	if lignes := strings.Split(strings.TrimSuffix(trame, "\n\n"), "\n"); len(lignes) != 2 {
		t.Errorf("trame de %d lignes: %q", len(lignes), trame)
	}
	// Et la clé n'apparaît évidemment pas en clair dans le flux.
	if strings.Contains(trame, "BEGIN OPENSSH PRIVATE KEY") {
		t.Error("la clé privée est écrite en clair dans le flux")
	}
}

// L'ADR-0018 interdit de journaliser le corps des messages. Un `%v` bien
// intentionné est le chemin le plus court vers l'inverse.
func TestSecretsMasquesDansLesImpressions(t *testing.T) {
	o := ordre()
	m := dispatch.Message{Kind: dispatch.KindOrder, Order: &o}

	imprime := func(format string, v any) string { return fmt.Sprintf(format, v) }
	rendus := map[string]string{
		"order %v":   imprime("%v", o),
		"order %s":   imprime("%s", o),
		"order %+v":  imprime("%+v", o),
		"order %#v":  imprime("%#v", o),
		"message %v": imprime("%v", m),
		"secret %v":  imprime("%v", o.SSHKey),
		"secret %s":  imprime("%s", o.GitToken),
	}
	secrets := map[string]string{
		"la clé privée":    "BEGIN OPENSSH PRIVATE KEY",
		"le jeton git":     "ght_0123456789abcdef",
		"la variable":      "s3cr3t",
		"la clé en base64": "b3BlbnNzaC1rZXktdjEA",
	}
	for forme, rendu := range rendus {
		for quoi, secret := range secrets {
			if strings.Contains(rendu, secret) {
				t.Errorf("%s apparaît sous %s: %s", quoi, forme, rendu)
			}
		}
	}

	// Mais le résumé reste utile : une structure qui ne s'imprime qu'en
	// « [supprimé] » finit décomposée à la main dans un log, et c'est là que la
	// fuite arrive.
	resume := fmt.Sprintf("%v", o)
	for _, attendu := range []string{o.RunRef, "site.yml", "staging"} {
		if !strings.Contains(resume, attendu) {
			t.Errorf("le résumé ne porte pas %q: %s", attendu, resume)
		}
	}
}

func TestDecodeurIgnoreCommentairesEtTramesDeMaintien(t *testing.T) {
	o := ordre()
	var buf bytes.Buffer
	// Ce qu'un proxy et un maintien de connexion produisent réellement.
	buf.WriteString(": ouverture du canal\n\n")
	buf.WriteString("\n")
	if err := dispatch.Encode(&buf, dispatch.Message{Kind: dispatch.KindHello}); err != nil {
		t.Fatalf("Encode(hello): %v", err)
	}
	buf.WriteString(": maintien\n\n")
	if err := dispatch.Encode(&buf, dispatch.Message{Kind: dispatch.KindOrder, Order: &o}); err != nil {
		t.Fatalf("Encode(order): %v", err)
	}
	if err := dispatch.Encode(&buf, dispatch.Message{Kind: dispatch.KindCancel, Reason: "user_interrupt"}); err != nil {
		t.Fatalf("Encode(cancel): %v", err)
	}

	d := dispatch.NewDecoder(&buf)
	attendus := []dispatch.Kind{dispatch.KindHello, dispatch.KindOrder, dispatch.KindCancel}
	for i, k := range attendus {
		m, err := d.Next()
		if err != nil {
			t.Fatalf("message %d: %v", i, err)
		}
		if m.Kind != k {
			t.Errorf("message %d de type %q, attendu %q", i, m.Kind, k)
		}
		if k == dispatch.KindCancel && m.Reason != "user_interrupt" {
			t.Errorf("annulation sans motif: %q", m.Reason)
		}
	}
	if _, err := d.Next(); !errors.Is(err, io.EOF) {
		t.Errorf("fin de flux = %v, attendu io.EOF", err)
	}
}

func TestEncodageRefuseCeQuiCasseLeFlux(t *testing.T) {
	cas := map[string]dispatch.Message{
		"message sans type":       {Order: &dispatch.Order{RunRef: "x"}},
		"type avec saut de ligne": {Kind: dispatch.Kind("order\ndata: injecté")},
		"message au-delà du plafond": {
			Kind:  dispatch.KindOrder,
			Order: &dispatch.Order{RunRef: "x", SSHKey: dispatch.Secret(bytes.Repeat([]byte("k"), 1024*1024))},
		},
	}
	for nom, m := range cas {
		var buf bytes.Buffer
		if err := dispatch.Encode(&buf, m); err == nil {
			t.Errorf("un %s a été encodé", nom)
		} else if buf.Len() != 0 {
			t.Errorf("un %s a été partiellement écrit: %q", nom, buf.String())
		}
	}
}

func TestDecodageRefuseUnFluxMalForme(t *testing.T) {
	cas := map[string]string{
		"données sans type":  "data: {}\n\n",
		"corps illisible":    "event: order\ndata: {ceci n'est pas du json\n\n",
		"ordre sans contenu": "event: order\ndata: {}\n\n",
	}
	for nom, flux := range cas {
		if _, err := dispatch.NewDecoder(strings.NewReader(flux)).Next(); err == nil {
			t.Errorf("un flux avec %s a été accepté", nom)
		}
	}
}

// Étage 1 de l'ADR-0010 : ni base, ni cluster.
//
// La propriété testée est la TRADUCTION d'un descripteur reçu vers le
// descripteur du cœur — et en particulier ce que le handler ne juge PLUS
// lui-même depuis l'ADR-0036 : la forme de l'adresse.

package api

import (
	"strings"
	"testing"

	"github.com/jijins1/warden/internal/server/internal/core"
)

// Un NOM en adresse traverse. C'est le renversement de l'ADR-0036 : le handler
// posait un `netip.ParseAddr` qui refusait cette requête en 400.
func TestUnNomDHoteEstAccepteEnAdresse(t *testing.T) {
	h, err := versManagedHost(hostPayload{
		Name: "kodi", Address: "kodi.lan", Login: "ruokki",
	}, "prod", core.HostDeclared)
	if err != nil {
		t.Fatalf("descripteur refusé : %v", err)
	}
	if h.Address != "kodi.lan" {
		t.Errorf("adresse = %q, attendu kodi.lan", h.Address)
	}
	// Le port absent prend le défaut, comme avant.
	if h.Port != core.PortSSHParDefaut {
		t.Errorf("port = %d, attendu %d", h.Port, core.PortSSHParDefaut)
	}
}

// Ce qui ne peut désigner aucune machine reste refusé, et c'est désormais le
// CŒUR qui le dit : le handler n'a plus de message à lui sur ce sujet, donc plus
// d'occasion d'en faire diverger un.
func TestUneAdresseInformeResteRefusee(t *testing.T) {
	for nom, adresse := range map[string]string{
		"vide":              "",
		"avec un espace":    "db prod 02",
		"toutes interfaces": "0.0.0.0",
		"une URL":           "https://db",
	} {
		t.Run(nom, func(t *testing.T) {
			_, err := versManagedHost(hostPayload{
				Name: "db", Address: adresse, Login: "ansible",
			}, "prod", core.HostDeclared)
			if err == nil {
				t.Fatalf("adresse %q acceptée à tort", adresse)
			}
			// Le message nomme la machine : c'est ce qui permet de corriger la
			// bonne ligne d'un fichier de déclaration.
			if !strings.Contains(err.Error(), "db") {
				t.Errorf("l'erreur ne nomme pas la machine : %v", err)
			}
		})
	}
}

// L'environnement du document l'emporte sur celui de la ligne. Propriété
// antérieure à l'ADR-0036, vérifiée ici parce que rien ne la couvrait.
func TestLEnvironnementDuDocumentLEmporte(t *testing.T) {
	h, err := versManagedHost(hostPayload{
		Name: "db", Environment: "staging", Address: "10.0.0.9", Login: "ansible",
	}, "prod", core.HostDeclared)
	if err != nil {
		t.Fatalf("descripteur refusé : %v", err)
	}
	if h.Environment != "prod" {
		t.Errorf("environnement = %q, attendu prod", h.Environment)
	}
}

// Le MODE n'est jamais lu du corps : une requête qui pourrait se déclarer
// `assisted` ferait croire que warden s'est connecté à une machine qu'il n'a
// jamais vue.
func TestLeModeDuCorpsEstIgnore(t *testing.T) {
	h, err := versManagedHost(hostPayload{
		Name: "db", Address: "10.0.0.9", Login: "ansible", Mode: "assisted",
	}, "prod", core.HostDeclared)
	if err != nil {
		t.Fatalf("descripteur refusé : %v", err)
	}
	if h.Mode != core.HostDeclared {
		t.Errorf("mode = %q, attendu declared", h.Mode)
	}
}

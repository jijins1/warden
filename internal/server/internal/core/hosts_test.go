package core_test

import (
	"strings"
	"testing"

	"github.com/jijins1/warden/internal/server/internal/core"
)

// L'adresse d'une machine accepte une IP ou un nom (ADR-0036).
//
// Ce que ce test établit n'est PAS qu'un nom résout : la validation n'en sait
// rien, et c'est le résolveur du pod qui tranchera au moment de la connexion.
// Il établit la frontière entre ce qui peut être une désignation de machine et
// ce qui ne peut pas en être une.
func TestValiderAdresse(t *testing.T) {
	cas := map[string]struct {
		adresse string
		accepte bool
	}{
		"IPv4":                    {"10.20.0.42", true},
		"IPv6":                    {"2001:db8::42", true},
		"IPv6 abrégée":            {"::1", true},
		"nom simple":              {"kodi", true},
		"FQDN":                    {"db-prod-02.example.com", true},
		"nom à chiffres":          {"node1.rack2", true},
		"tiret intérieur":         {"db-prod-02", true},
		"vide":                    {"", false},
		"toutes interfaces":       {"0.0.0.0", false},
		"multicast":               {"224.0.0.1", false},
		"multicast v6":            {"ff02::1", false},
		"espace":                  {"db prod", false},
		"saut de ligne":           {"db\n02", false},
		"URL":                     {"https://db", false},
		"souligné":                {"db_prod", false},
		"tiret en tête":           {"-db", false},
		"tiret en queue":          {"db-", false},
		"point en tête":           {".db", false},
		"deux points consécutifs": {"db..prod", false},
		"trop longue":             {strings.Repeat("a", 254), false},
	}

	for nom, c := range cas {
		t.Run(nom, func(t *testing.T) {
			err := core.ValiderAdresse(c.adresse)
			if c.accepte && err != nil {
				t.Fatalf("adresse %q refusée : %v", c.adresse, err)
			}
			if !c.accepte && err == nil {
				t.Fatalf("adresse %q acceptée à tort", c.adresse)
			}
		})
	}
}

// Un nom d'hôte traverse `ManagedHost.Validate`, là où la migration 00008 et le
// type `netip.Addr` le refusaient. C'est le changement de l'ADR-0036, vu depuis
// le descripteur complet.
func TestUneMachineSeValideAvecUnNomPourAdresse(t *testing.T) {
	h := core.ManagedHost{
		Name: "kodi", Environment: "prod", Address: "kodi.lan",
		Port: core.PortSSHParDefaut, Login: "ruokki", Mode: core.HostDeclared,
	}
	if err := h.Validate(); err != nil {
		t.Fatalf("machine refusée : %v", err)
	}
}

// Le message d'erreur NOMME la machine. C'est la raison d'être de cette
// validation au bord : une violation découverte par PostgreSQL ne dirait pas
// quelle ligne d'un fichier de déclaration est en cause.
func TestUneAdresseRefuseeNommeLaMachine(t *testing.T) {
	h := core.ManagedHost{
		Name: "kodi", Environment: "prod", Address: "0.0.0.0",
		Port: core.PortSSHParDefaut, Login: "ruokki", Mode: core.HostDeclared,
	}
	err := h.Validate()
	if err == nil {
		t.Fatal("0.0.0.0 acceptée à tort")
	}
	if !strings.Contains(err.Error(), "kodi") {
		t.Errorf("l'erreur ne nomme pas la machine : %v", err)
	}
}

// La cible d'un enrôlement réutilise la validation d'une machine : les faire
// diverger produirait un enrôlement qui réussit et une déclaration que la base
// refuse.
func TestUneCibleDEnrolementAccepteUnNom(t *testing.T) {
	cible := core.EnrollTarget{
		Host: "kodi", Address: "kodi.lan", Port: 22, Login: "ruokki",
	}
	if err := cible.Validate(); err != nil {
		t.Fatalf("cible refusée : %v", err)
	}

	informe := core.EnrollTarget{
		Host: "kodi", Address: "kodi lan", Port: 22, Login: "ruokki",
	}
	if err := informe.Validate(); err == nil {
		t.Fatal("une adresse informe a été acceptée")
	}
}

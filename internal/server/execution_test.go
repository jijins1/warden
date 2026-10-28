package server

import "testing"

// Sans aucune variable, le mode est celui d'avant l'ADR-0047 : un déploiement
// existant ne doit rien voir changer.
func TestLeModeParDefautEstKubernetes(t *testing.T) {
	t.Setenv(EnvOrchestrator, "")
	t.Setenv(EnvSecrets, "")
	m, err := lireModes()
	if err != nil {
		t.Fatal(err)
	}
	if m.orchestrateur != modeKubernetes || m.secrets != modeKubernetes {
		t.Errorf("modes : %+v", m)
	}
}

func TestLesCombinaisonsDeModes(t *testing.T) {
	cas := []struct {
		orchestrateur, secrets string
		ok                     bool
	}{
		{"kubernetes", "kubernetes", true},
		{"kubernetes", "vault", true},
		{"docker", "vault", true},
		// Sans cluster, pas de Secrets Kubernetes où ranger les clés.
		{"docker", "kubernetes", false},
		{"docker", "", false},
		// Une faute de frappe ne retombe pas en silence sur Kubernetes.
		{"dokcer", "vault", false},
		{"kubernetes", "vaut", false},
	}
	for _, c := range cas {
		t.Run(c.orchestrateur+"+"+c.secrets, func(t *testing.T) {
			t.Setenv(EnvOrchestrator, c.orchestrateur)
			t.Setenv(EnvSecrets, c.secrets)
			_, err := lireModes()
			if (err == nil) != c.ok {
				t.Errorf("erreur = %v, acceptation attendue : %v", err, c.ok)
			}
		})
	}
}

// Le mode Docker sans réseau échoue au démarrage, pas au premier run.
func TestLeModeDockerExigeSonReseau(t *testing.T) {
	t.Setenv(EnvDockerNetwork, "")
	if _, err := lanceurDocker("http://d", "http://i", 0); err == nil {
		t.Error("mode Docker accepté sans réseau")
	}
}

// Seule une adresse http hors de la boucle locale fait passer les secrets en
// clair.
func TestVaultEnClair(t *testing.T) {
	cas := map[string]bool{
		"http://vault:8200":     true,
		"http://10.0.0.5:8200":  true,
		"https://vault:8200":    false,
		"http://localhost:8200": false,
		"http://127.0.0.1:8200": false,
		"http://[::1]:8200":     false,
	}
	for adresse, attendu := range cas {
		if got := vaultEnClair(adresse); got != attendu {
			t.Errorf("vaultEnClair(%q) = %v, attendu %v", adresse, got, attendu)
		}
	}
}

// Le mode Vault sans identifiants échoue au démarrage.
func TestLeModeVaultExigeSesIdentifiants(t *testing.T) {
	t.Setenv(EnvVaultAddr, "http://vault:8200")
	t.Setenv(EnvVaultRoleID, "")
	t.Setenv(EnvVaultSecretIDFile, "")
	if _, err := ouvrirVault(nil); err == nil {
		t.Error("mode Vault accepté sans role_id ni secret_id")
	}
}

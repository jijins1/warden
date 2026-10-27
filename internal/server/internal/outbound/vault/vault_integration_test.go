//go:build integration

// Étage 2 de l'ADR-0010 : un Vault réel, testcontainers.
//
// Ce que le faux Vault de l'étage 1 ne peut pas montrer : que la policy
// documentée par l'ADR-0047 — sans `list`, `delete` sur `metadata/` seulement
// — suffit au client, et que la destruction ne laisse VRAIMENT rien de
// récupérable.

package vault

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/jijins1/warden/internal/server/internal/core"
)

const (
	imageVault  = "hashicorp/vault:1.18.3"
	jetonRacine = "racine-de-test"
)

// policyWarden est celle que l'ADR-0047 décrit : pas de `list` sur la
// consigne, et la seule suppression permise est la destruction.
const policyWarden = `
path "warden-transit/data/*"     { capabilities = ["create", "update", "read"] }
path "warden-transit/metadata/*" { capabilities = ["delete"] }
path "warden/data/*"             { capabilities = ["create", "update", "read"] }
`

// vaultReel démarre Vault en mode dev, le configure comme le ferait le
// déploiement, et rend l'adresse et un Client authentifié par AppRole.
func vaultReel(t *testing.T) (string, *Client) {
	t.Helper()
	ctx := t.Context()

	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        imageVault,
			ExposedPorts: []string{"8200/tcp"},
			Env:          map[string]string{"VAULT_DEV_ROOT_TOKEN_ID": jetonRacine},
			Cmd:          []string{"server", "-dev", "-dev-listen-address=0.0.0.0:8200"},
			WaitingFor:   wait.ForHTTP("/v1/sys/health").WithPort("8200/tcp").WithStartupTimeout(time.Minute),
		},
		Started: true,
	})
	if err != nil {
		t.Skipf("Vault indisponible : %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(c) })

	hote, err := c.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := c.MappedPort(ctx, "8200/tcp")
	if err != nil {
		t.Fatal(err)
	}
	addr := fmt.Sprintf("http://%s:%s", hote, port.Port())

	racine(t, addr, http.MethodPost, "sys/mounts/warden-transit",
		map[string]any{"type": "kv", "options": map[string]string{"version": "2"}})
	racine(t, addr, http.MethodPost, "warden-transit/config", map[string]any{"max_versions": 1})
	racine(t, addr, http.MethodPost, "sys/mounts/warden",
		map[string]any{"type": "kv", "options": map[string]string{"version": "2"}})
	racine(t, addr, http.MethodPost, "sys/auth/approle", map[string]any{"type": "approle"})
	racine(t, addr, http.MethodPut, "sys/policies/acl/warden", map[string]any{"policy": policyWarden})
	racine(t, addr, http.MethodPost, "auth/approle/role/warden",
		map[string]any{"token_policies": []string{"warden"}, "token_ttl": "1h"})

	var role struct {
		Data struct {
			RoleID string `json:"role_id"`
		} `json:"data"`
	}
	lireJSON(t, racine(t, addr, http.MethodGet, "auth/approle/role/warden/role-id", nil), &role)
	var secret struct {
		Data struct {
			SecretID string `json:"secret_id"`
		} `json:"data"`
	}
	lireJSON(t, racine(t, addr, http.MethodPost, "auth/approle/role/warden/secret-id", map[string]any{}), &secret)

	fichier := filepath.Join(t.TempDir(), "secret_id")
	if err := os.WriteFile(fichier, []byte(secret.Data.SecretID), 0o600); err != nil {
		t.Fatal(err)
	}
	kv, err := New(Config{Addr: addr, RoleID: role.Data.RoleID, SecretIDFile: fichier})
	if err != nil {
		t.Fatal(err)
	}
	return addr, kv
}

// racine fait un appel avec le jeton racine et échoue sur tout statut
// d'erreur.
func racine(t *testing.T, addr, methode, chemin string, corps any) *http.Response {
	t.Helper()
	rep := appelRacine(t, addr, methode, chemin, corps)
	if rep.StatusCode >= 300 {
		t.Fatalf("%s %s : statut %d", methode, chemin, rep.StatusCode)
	}
	return rep
}

func appelRacine(t *testing.T, addr, methode, chemin string, corps any) *http.Response {
	t.Helper()
	var entree bytes.Buffer
	if corps != nil {
		_ = json.NewEncoder(&entree).Encode(corps)
	}
	req, err := http.NewRequestWithContext(context.Background(), methode, addr+"/v1/"+chemin, &entree)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Vault-Token", jetonRacine)
	rep, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rep.Body.Close() })
	return rep
}

func lireJSON(t *testing.T, rep *http.Response, v any) {
	t.Helper()
	if err := json.NewDecoder(rep.Body).Decode(v); err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationLaConsigneDetruitSansRienLaisser(t *testing.T) {
	addr, kv := vaultReel(t)
	c, err := NewConsigne(kv, "warden-transit", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()

	if err := c.Stash(ctx, "01J9RUNREF-become", []byte("motdepasse")); err != nil {
		t.Fatalf("Stash : %v", err)
	}
	v, err := c.Take(ctx, "01J9RUNREF-become")
	if err != nil || string(v) != "motdepasse" {
		t.Fatalf("Take : %q, %v", v, err)
	}
	if _, err := c.Take(ctx, "01J9RUNREF-become"); !errors.Is(err, core.ErrNoStash) {
		t.Errorf("seconde lecture : %v", err)
	}

	// Même le jeton racine ne retrouve rien : ni version, ni métadonnée à
	// `undelete`.
	if rep := appelRacine(t, addr, http.MethodGet, "warden-transit/metadata/01J9RUNREF-become", nil); rep.StatusCode != http.StatusNotFound {
		t.Errorf("métadonnées encore présentes après lecture : statut %d", rep.StatusCode)
	}
}

func TestIntegrationLesClesSePosentEtSeRelisent(t *testing.T) {
	_, kv := vaultReel(t)
	ctx := t.Context()

	valeur := []byte{0x00, 0xff, 'k'}
	if err := kv.Ecrire(ctx, "warden", "warden-key-prod", valeur); err != nil {
		t.Fatalf("Ecrire : %v", err)
	}
	lu, trouve, err := kv.Lire(ctx, "warden", "warden-key-prod")
	if err != nil || !trouve || !bytes.Equal(lu, valeur) {
		t.Errorf("Lire : %v, %v, %v", lu, trouve, err)
	}
	if _, trouve, err := kv.Lire(ctx, "warden", "absente"); err != nil || trouve {
		t.Errorf("clé absente : trouve=%v err=%v", trouve, err)
	}
}

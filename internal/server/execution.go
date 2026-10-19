package server

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	dockerclient "github.com/moby/moby/client"

	"github.com/jijins1/warden/internal/server/internal/core"
	"github.com/jijins1/warden/internal/server/internal/outbound/docker"
	"github.com/jijins1/warden/internal/server/internal/outbound/keyinventory"
	"github.com/jijins1/warden/internal/server/internal/outbound/store"
	"github.com/jijins1/warden/internal/server/internal/outbound/vault"
	"github.com/jijins1/warden/internal/server/internal/outbound/vaultinventory"
)

// Mode d'exécution (ADR-0047) : qui lance les runners, et où vivent les
// secrets. Les deux se choisissent au démarrage, et leur défaut est
// Kubernetes : un déploiement existant, dont la chart ne pose aucune de ces
// variables, démarre exactement comme avant.
const (
	EnvOrchestrator = "WARDEN_ORCHESTRATOR" // `kubernetes` (défaut) ou `docker`
	EnvSecrets      = "WARDEN_SECRETS"      // `kubernetes` (défaut) ou `vault`

	// Mode Docker. L'adresse du démon vient de DOCKER_HOST, comme pour tout
	// client Docker : elle pointe le proxy filtrant du socket, jamais le
	// socket lui-même (ADR-0047 §6).
	EnvDockerInstance         = "WARDEN_DOCKER_INSTANCE"           // label d'instance, `warden` par défaut
	EnvDockerNetwork          = "WARDEN_DOCKER_NETWORK"            // réseau Docker des runners
	EnvDockerRegistryAuthFile = "WARDEN_DOCKER_REGISTRY_AUTH_FILE" // en-tête X-Registry-Auth, facultatif

	// Vault. Le `secret_id` est le seul secret à fournir au déploiement, et
	// il vient d'un fichier : relu à chaque authentification, il tourne sans
	// redémarrage.
	EnvVaultAddr           = "WARDEN_VAULT_ADDR"
	EnvVaultRoleID         = "WARDEN_VAULT_ROLE_ID"
	EnvVaultSecretIDFile   = "WARDEN_VAULT_SECRET_ID_FILE"
	EnvVaultCACert         = "WARDEN_VAULT_CACERT"        // facultatif
	EnvVaultAppRoleMount   = "WARDEN_VAULT_APPROLE_MOUNT" // `approle` par défaut
	EnvVaultKVMount        = "WARDEN_VAULT_KV_MOUNT"      // clés d'accès et de vault, `warden` par défaut
	EnvVaultTransitMount   = "WARDEN_VAULT_TRANSIT_MOUNT" // consigne, `warden-transit` par défaut
	EnvVaultTransitTTLSecs = "WARDEN_VAULT_TRANSIT_TTL_SECONDS"
)

const (
	modeKubernetes = "kubernetes"
	modeDocker     = "docker"
	modeVault      = "vault"
)

// modes est le mode d'exécution lu au démarrage.
type modes struct {
	orchestrateur string
	secrets       string
}

// lireModes lit et valide le mode d'exécution. Une valeur inconnue fait
// échouer le démarrage : une faute de frappe ne doit pas retomber en silence
// sur Kubernetes.
func lireModes() (modes, error) {
	m := modes{
		orchestrateur: valeurOuDefaut(EnvOrchestrator, modeKubernetes),
		secrets:       valeurOuDefaut(EnvSecrets, modeKubernetes),
	}
	if m.orchestrateur != modeKubernetes && m.orchestrateur != modeDocker {
		return modes{}, fmt.Errorf("%s=%q : `%s` ou `%s` attendu",
			EnvOrchestrator, m.orchestrateur, modeKubernetes, modeDocker)
	}
	if m.secrets != modeKubernetes && m.secrets != modeVault {
		return modes{}, fmt.Errorf("%s=%q : `%s` ou `%s` attendu",
			EnvSecrets, m.secrets, modeKubernetes, modeVault)
	}
	if m.orchestrateur == modeDocker && m.secrets == modeKubernetes {
		// Sans cluster, il n'y a pas de Secrets Kubernetes où ranger les clés.
		return modes{}, fmt.Errorf("%s=%s exige %s=%s",
			EnvOrchestrator, modeDocker, EnvSecrets, modeVault)
	}
	return m, nil
}

// lanceurDocker construit l'adaptateur Docker. `deadline` à zéro laisse le
// défaut de l'adaptateur.
func lanceurDocker(dispatchURL, ingestURL string, deadline int64) (*docker.Docker, error) {
	reseau := os.Getenv(EnvDockerNetwork)
	if reseau == "" {
		return nil, fmt.Errorf("%s=%s exige %s", EnvOrchestrator, modeDocker, EnvDockerNetwork)
	}
	var auth string
	if chemin := os.Getenv(EnvDockerRegistryAuthFile); chemin != "" {
		brut, err := os.ReadFile(chemin) // #nosec G304 G703 -- chemin fourni par le déploiement
		if err != nil {
			return nil, fmt.Errorf("%s : %w", EnvDockerRegistryAuthFile, err)
		}
		auth = strings.TrimSpace(string(brut))
	}

	cli, err := dockerclient.New(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("client Docker : %w", err)
	}
	return docker.New(cli, docker.Config{
		Instance:        valeurOuDefaut(EnvDockerInstance, "warden"),
		Network:         reseau,
		DeadlineSeconds: deadline,
		DispatchURL:     dispatchURL,
		IngestURL:       ingestURL,
		RegistryAuth:    auth,
	})
}

// magasinsVault rassemble ce que le mode `vault` fournit : la consigne et les
// deux inventaires de clés.
type magasinsVault struct {
	consigne *vault.Consigne
	cles     *keyinventory.Vault
	vault    *vaultinventory.Vault
}

func ouvrirVault(st *store.Store) (magasinsVault, error) {
	kv, err := vault.New(vault.Config{
		Addr:         os.Getenv(EnvVaultAddr),
		AppRoleMount: os.Getenv(EnvVaultAppRoleMount),
		RoleID:       os.Getenv(EnvVaultRoleID),
		SecretIDFile: os.Getenv(EnvVaultSecretIDFile),
		CACertFile:   os.Getenv(EnvVaultCACert),
	})
	if err != nil {
		return magasinsVault{}, fmt.Errorf("%s=%s : %w (voir %s, %s, %s)",
			EnvSecrets, modeVault, err, EnvVaultAddr, EnvVaultRoleID, EnvVaultSecretIDFile)
	}

	ttl := time.Duration(0)
	if brut := os.Getenv(EnvVaultTransitTTLSecs); brut != "" {
		n, err := strconv.Atoi(brut)
		if err != nil || n <= 0 {
			return magasinsVault{}, fmt.Errorf("%s=%q : un nombre de SECONDES strictement positif est attendu",
				EnvVaultTransitTTLSecs, brut)
		}
		ttl = time.Duration(n) * time.Second
	}
	consigne, err := vault.NewConsigne(kv, valeurOuDefaut(EnvVaultTransitMount, "warden-transit"), ttl)
	if err != nil {
		return magasinsVault{}, err
	}

	montage := valeurOuDefaut(EnvVaultKVMount, "warden")
	cles, err := keyinventory.NewVault(kv, montage, st)
	if err != nil {
		return magasinsVault{}, err
	}
	vi, err := vaultinventory.NewVault(kv, montage, st)
	if err != nil {
		return magasinsVault{}, err
	}
	return magasinsVault{consigne: consigne, cles: cles, vault: vi}, nil
}

// Les adaptateurs satisfont bien les ports qu'on leur fait porter.
var (
	_ core.PodLauncher  = (*docker.Docker)(nil)
	_ core.PodsObserves = (*docker.Docker)(nil)
	_ core.SecretStash  = (*vault.Consigne)(nil)
	_ core.KeyIssuer    = (*keyinventory.Vault)(nil)
	_ core.KeyStore     = (*keyinventory.Vault)(nil)
	_ core.VaultIssuer  = (*vaultinventory.Vault)(nil)
	_ core.VaultStore   = (*vaultinventory.Vault)(nil)
)

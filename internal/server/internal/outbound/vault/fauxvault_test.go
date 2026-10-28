// Étage 1 de l'ADR-0010 : un faux Vault `httptest`, aucun démon.
//
// Il ne reproduit que ce que le client utilise — login AppRole,
// renouvellement, et KV v2 en `data/` et `metadata/` — et compte ce qui
// importe aux tests : les authentifications, les effacements logiques.

package vault

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const (
	roleTest    = "role-warden"
	secretTest  = "secret-warden"
	montageTest = "warden-transit"
)

type fauxVault struct {
	mu sync.Mutex

	bail         int  // lease_duration rendu au login, en secondes
	renouvelable bool // renewable rendu au login

	jetons             map[string]bool
	n                  int
	logins             int
	renews             int
	kv                 map[string]map[string]any // "<montage>/<chemin>" -> champs
	effacesLogiquement int
}

func nouveauFauxVault(t *testing.T) (*fauxVault, *httptest.Server) {
	t.Helper()
	f := &fauxVault{jetons: map[string]bool{}, kv: map[string]map[string]any{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

// client construit un Client vers le faux Vault, avec le secret_id dans un
// fichier temporaire.
func client(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	fichier := filepath.Join(t.TempDir(), "secret_id")
	if err := os.WriteFile(fichier, []byte(secretTest+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := New(Config{Addr: srv.URL, RoleID: roleTest, SecretIDFile: fichier})
	if err != nil {
		t.Fatalf("New : %v", err)
	}
	return c
}

func (f *fauxVault) revoquerTout() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.jetons = map[string]bool{}
}

func (f *fauxVault) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	chemin := strings.TrimPrefix(r.URL.Path, "/v1/")
	switch chemin {
	case "auth/approle/login":
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in["role_id"] != roleTest || in["secret_id"] != secretTest {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.logins++
		f.n++
		jeton := fmt.Sprintf("hvs.%d", f.n)
		f.jetons[jeton] = true
		f.repondreAuth(w, jeton)
		return
	case "auth/token/renew-self":
		jeton := r.Header.Get("X-Vault-Token")
		if !f.jetons[jeton] || !f.renouvelable {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		f.renews++
		f.repondreAuth(w, jeton)
		return
	}

	if !f.jetons[r.Header.Get("X-Vault-Token")] {
		w.WriteHeader(http.StatusForbidden)
		return
	}

	montage, reste, _ := strings.Cut(chemin, "/")
	espace, cle, _ := strings.Cut(reste, "/")
	id := montage + "/" + cle

	switch {
	case espace == "data" && r.Method == http.MethodGet:
		champs, ok := f.kv[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"data": champs}})
	case espace == "data" && r.Method == http.MethodPost:
		var in struct {
			Data map[string]any `json:"data"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.kv[id] = in.Data
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"version":1}}`))
	case espace == "data" && r.Method == http.MethodDelete:
		// L'effacement logique : la valeur resterait récupérable. Compté pour
		// que les tests prouvent qu'il n'est jamais employé.
		f.effacesLogiquement++
		w.WriteHeader(http.StatusNoContent)
	case espace == "metadata" && r.Method == http.MethodDelete:
		delete(f.kv, id)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (f *fauxVault) repondreAuth(w http.ResponseWriter, jeton string) {
	_ = json.NewEncoder(w).Encode(map[string]any{"auth": map[string]any{
		"client_token":   jeton,
		"lease_duration": f.bail,
		"renewable":      f.renouvelable,
	}})
}

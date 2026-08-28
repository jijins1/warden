// Étage 1 de l'ADR-0010 : ni base, ni conteneur, ni cluster.
package core_test

import (
	"strings"
	"testing"

	"github.com/jijins1/warden/internal/config"
	"github.com/jijins1/warden/internal/server/internal/core"
)

// L'ordre de composition de l'ADR-0009 : la configuration du commit d'abord, les
// flags explicites par-dessus. Sans politique serveur, la couche 1 est neutre —
// mais l'ORDRE des deux autres est ce qui se teste ici.
func TestComposerFlagsPriment(t *testing.T) {
	projet := config.Project{
		DefaultEnv: "staging",
		Inventory:  "inventories/{{ env }}",
	}

	// Rien d'explicite : la configuration du commit décide.
	eff, err := core.Composer(projet, core.Requete{})
	if err != nil {
		t.Fatalf("Composer: %v", err)
	}
	if eff.Environment != "staging" {
		t.Errorf("environnement = %q, veut staging", eff.Environment)
	}
	if eff.Inventory != "inventories/staging" {
		t.Errorf("inventaire = %q, le template doit être résolu", eff.Inventory)
	}

	// `--env` explicite : il prime, ET il change l'inventaire par templatisation.
	eff, err = core.Composer(projet, core.Requete{Environment: "prod"})
	if err != nil {
		t.Fatalf("Composer: %v", err)
	}
	if eff.Environment != "prod" {
		t.Errorf("environnement = %q, veut prod", eff.Environment)
	}
	if eff.Inventory != "inventories/prod" {
		t.Errorf("inventaire = %q : le template suit l'environnement effectif", eff.Inventory)
	}

	// `--inventory` explicite : il prime sur le template, qui n'est alors pas
	// résolu du tout.
	eff, err = core.Composer(projet, core.Requete{Inventory: "inventories/special"})
	if err != nil {
		t.Fatalf("Composer: %v", err)
	}
	if eff.Inventory != "inventories/special" {
		t.Errorf("inventaire = %q, veut celui du flag", eff.Inventory)
	}
}

// Un inventaire templatisé sans environnement ne peut pas se résoudre. Le
// refuser vaut mieux que produire `inventories/`, qu'Ansible accepterait comme
// inventaire de type répertoire — donc ça marcherait, en désignant autre chose.
func TestComposerRefuseUnTemplateSansEnvironnement(t *testing.T) {
	_, err := core.Composer(config.Project{Inventory: "inventories/{{ env }}"}, core.Requete{})
	if err == nil {
		t.Fatal("un template sans environnement doit être refusé")
	}
	if !strings.Contains(err.Error(), "environnement") {
		t.Errorf("le message doit nommer la cause : %v", err)
	}
}

// Les NOMS viennent du commit, les VALEURS du client (ADR-0009). Une valeur
// envoyée pour un nom que le commit ne déclare pas n'est pas transmise : le
// fichier versionné est l'allowlist.
func TestComposerForwardEnvCroiseNomsEtValeurs(t *testing.T) {
	projet := config.Project{ForwardEnv: []string{"VAULT_ADDR", "APP_VERSION", "ABSENTE"}}

	eff, err := core.Composer(projet, core.Requete{
		Environment: "staging",
		ForwardEnv: map[string]string{
			"VAULT_ADDR":  "https://vault",
			"APP_VERSION": "1.2.3",
			"CLANDESTINE": "jamais transmise",
		},
	})
	if err != nil {
		t.Fatalf("Composer: %v", err)
	}

	if len(eff.ForwardEnv) != 2 {
		t.Fatalf("2 variables attendues, %d transmises : %v", len(eff.ForwardEnv), noms(eff.ForwardEnv))
	}
	if eff.ForwardEnv["VAULT_ADDR"] != "https://vault" {
		t.Errorf("VAULT_ADDR = %q", eff.ForwardEnv["VAULT_ADDR"])
	}
	// Déclarée mais sans valeur dans le shell : absente, pas vide. Transmettre
	// une chaîne vide écraserait la valeur que la cible pourrait avoir.
	if _, y := eff.ForwardEnv["ABSENTE"]; y {
		t.Error("une variable déclarée mais non fournie ne doit pas être transmise")
	}
	// Non déclarée dans le commit : jamais transmise, quoi que le client envoie.
	if _, y := eff.ForwardEnv["CLANDESTINE"]; y {
		t.Error("une variable non déclarée dans le commit ne doit jamais passer")
	}
}

// MESURÉ : `ANSIBLE_CALLBACK_PLUGINS` posée dans `env/envvars` préfixe le
// dossier de plugins de l'utilisateur devant celui d'ansible-runner, qui
// résout `awx_display` par nom et charge donc le faux. Le flux d'events se vide
// sans qu'aucune erreur ne soit émise : c'est la variable qui tue le produit.
func TestComposerRefuseLesNomsReserves(t *testing.T) {
	for _, nom := range core.NomsReserves {
		projet := config.Project{ForwardEnv: []string{nom}}
		_, err := core.Composer(projet, core.Requete{
			Environment: "staging",
			ForwardEnv:  map[string]string{nom: "peu importe"},
		})
		if err == nil {
			t.Errorf("%s doit être refusée", nom)
			continue
		}
		// Refus BRUYANT et non silencieux : une variable écartée sans le dire
		// laisserait l'utilisateur chercher pourquoi son playbook ne la voit pas.
		if !strings.Contains(err.Error(), nom) {
			t.Errorf("le message doit nommer %s : %v", nom, err)
		}
	}
}

// La liste réservée n'est pas décorative : elle DOIT contenir celle que la
// mesure a désignée. Si quelqu'un la retire, ce test le dit.
func TestNomsReservesContientLeTueurMesure(t *testing.T) {
	var trouve bool
	for _, n := range core.NomsReserves {
		if n == "ANSIBLE_CALLBACK_PLUGINS" {
			trouve = true
		}
	}
	if !trouve {
		t.Fatal("ANSIBLE_CALLBACK_PLUGINS est le seul tueur d'audit mesuré : il ne peut pas quitter la liste")
	}
}

// L'image du runner vient du commit (ADR-0014), jamais de la requête.
func TestComposerPrendLImageDuCommit(t *testing.T) {
	eff, err := core.Composer(
		config.Project{RunnerImage: "registry/equipe/runner:2.3.1"},
		core.Requete{Environment: "staging"},
	)
	if err != nil {
		t.Fatalf("Composer: %v", err)
	}
	if eff.RunnerImage != "registry/equipe/runner:2.3.1" {
		t.Errorf("image = %q", eff.RunnerImage)
	}
}

// Une configuration nulle et une requête nue composent sans rien inventer :
// c'est l'état d'un dépôt qui n'a pas encore adopté le fichier.
func TestComposerSurConfigurationVide(t *testing.T) {
	eff, err := core.Composer(config.Project{}, core.Requete{Environment: "staging"})
	if err != nil {
		t.Fatalf("Composer: %v", err)
	}
	if eff.Environment != "staging" || eff.Inventory != "" || len(eff.ForwardEnv) != 0 {
		t.Errorf("composition inattendue : %+v", eff)
	}
}

func noms(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// NomsTransmis TRIE, et ce n'est pas cosmétique : l'ordre d'itération d'une map
// varie d'un run à l'autre en Go, et un journal d'audit dont l'ordre bouge sans
// que rien n'ait changé se relit mal.
func TestNomsTransmisEstTrieEtStable(t *testing.T) {
	m := map[string]string{"VAULT_ADDR": "x", "APP_VERSION": "y", "ZONE": "z"}

	veut := []string{"APP_VERSION", "VAULT_ADDR", "ZONE"}
	for i := 0; i < 20; i++ {
		got := core.NomsTransmis(m)
		if len(got) != len(veut) {
			t.Fatalf("= %v, veut %v", got, veut)
		}
		for j := range veut {
			if got[j] != veut[j] {
				t.Fatalf("itération %d : = %v, veut %v", i, got, veut)
			}
		}
	}

	if len(core.NomsTransmis(nil)) != 0 {
		t.Error("une map nulle doit donner une liste vide")
	}
}

// Les deux consignes d'un même run ne doivent pas se marcher dessus.
func TestCleForwardEnvEstDistincteDeCleBecome(t *testing.T) {
	const run = "01JZQ000000000000000000ABC"
	if core.CleForwardEnv(run) == core.CleBecome(run) || core.CleForwardEnv(run) == run {
		t.Error("les clefs de consigne d'un même run doivent être distinctes")
	}
}

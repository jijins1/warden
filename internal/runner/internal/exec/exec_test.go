// Étage 1 de l'ADR-0010 : le pilotage contre un faux `ansible-runner`.
//
// Le faux est un script shell qui REJOUE une fixture mesurée, entrée standard
// comprise. Ce qu'on vérifie ici n'est donc pas Ansible — il a son propre
// projet — mais la couture : ce que le pilote écrit sur stdin, ce qu'il fait
// des trames de contrôle, et où il s'arrête.

package exec_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jijins1/warden/internal/events"
	"github.com/jijins1/warden/internal/runner/internal/exec"
)

// fauxRunner installe sur le PATH un `ansible-runner` qui recopie une fixture
// sur sa sortie, après avoir sauvegardé son entrée.
//
// Il renvoie le chemin du fichier où l'entrée a été capturée.
func fauxRunner(t *testing.T, fixture string, suffixe string) string {
	t.Helper()

	dir := t.TempDir()
	entree := filepath.Join(dir, "stdin.json")

	source, err := filepath.Abs("testdata/" + fixture)
	if err != nil {
		t.Fatalf("chemin de la fixture : %v", err)
	}

	script := "#!/bin/sh\ncat > " + entree + "\ncat " + source + "\n" + suffixe
	chemin := filepath.Join(dir, "ansible-runner")
	if err := os.WriteFile(chemin, []byte(script), 0o700); err != nil { // #nosec G306 -- un exécutable de test
		t.Fatalf("écriture du faux runner : %v", err)
	}

	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return entree
}

func config(t *testing.T) exec.Config {
	t.Helper()
	base := t.TempDir()
	cfg := exec.Config{
		Workspace:      filepath.Join(base, "src"),
		PrivateDataDir: filepath.Join(base, "pdd"),
		Playbook:       "site.yml",
		Inventory:      "inv/staging",
	}

	// Les deux fichiers existent RÉELLEMENT, parce que le pilote les vérifie
	// avant de lancer quoi que ce soit — et parce qu'un arbre matérialisé
	// depuis le commit audité les contient toujours. Les déclarer sans les
	// poser décrivait un état que la production n'atteint jamais.
	poser(t, cfg.Workspace, cfg.Playbook, "- hosts: all\n  tasks: []\n")
	poser(t, cfg.Workspace, cfg.Inventory, "[all]\nlocalhost\n")
	return cfg
}

// poser écrit un fichier de l'arbre audité, répertoires compris.
func poser(t *testing.T, racine, rel, contenu string) {
	t.Helper()
	chemin := filepath.Join(racine, rel)
	if err := os.MkdirAll(filepath.Dir(chemin), 0o750); err != nil {
		t.Fatalf("préparation de %s : %v", rel, err)
	}
	if err := os.WriteFile(chemin, []byte(contenu), 0o600); err != nil {
		t.Fatalf("écriture de %s : %v", rel, err)
	}
}

func lancer(t *testing.T, cfg exec.Config) ([]events.Event, exec.Statut, error) {
	t.Helper()
	r, err := exec.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var evs []events.Event
	statut, err := r.Run(t.Context(), func(e events.Event) { evs = append(evs, e) })
	return evs, statut, err
}

func TestUnRunReussiEstRapporteCommeTel(t *testing.T) {
	fauxRunner(t, "flux-succes.jsonl", "")

	evs, statut, err := lancer(t, config(t))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if statut != exec.StatutReussi {
		t.Errorf("statut = %q, attendu %q", statut, exec.StatutReussi)
	}
	if len(evs) == 0 {
		t.Fatal("aucun event émis")
	}
}

// Le statut vient du FLUX, pas du code de sortie : le §6.4 note que « l'exit
// code du pod dit que le processus est mort, pas ce que le playbook a fait ».
// Un playbook en échec fait sortir ansible-runner en non-zéro, et ce n'est PAS
// une panne du pilotage.
func TestUnPlaybookEnEchecNEstPasUnePanneDePilotage(t *testing.T) {
	fauxRunner(t, "flux-complet.jsonl", "exit 2\n")

	evs, statut, err := lancer(t, config(t))
	if err != nil {
		t.Fatalf("Run a rendu une erreur pour un playbook en échec : %v", err)
	}
	if statut != exec.StatutEchoue {
		t.Errorf("statut = %q, attendu %q", statut, exec.StatutEchoue)
	}
	if len(evs) == 0 {
		t.Fatal("aucun event émis")
	}
}

// Un processus qui meurt sans rien dire est en revanche une panne : sans
// statut, il n'y a rien à journaliser d'honnête.
func TestUnProcessusMuetEstUnePanne(t *testing.T) {
	dir := t.TempDir()
	chemin := filepath.Join(dir, "ansible-runner")
	if err := os.WriteFile(chemin, []byte("#!/bin/sh\ncat > /dev/null\nexit 3\n"), 0o700); err != nil { // #nosec G306
		t.Fatalf("écriture : %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, _, err := lancer(t, config(t))
	if err == nil {
		t.Fatal("un processus mort sans statut doit être une panne de pilotage")
	}
}

func TestUnBinaireAbsentEstUnePanneNommee(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	_, _, err := lancer(t, config(t))
	if err == nil {
		t.Fatal("un binaire absent doit remonter")
	}
	if !strings.Contains(err.Error(), exec.Binaire) {
		t.Errorf("erreur = %v, elle devrait nommer le binaire manquant", err)
	}
}

// Ce que le pilote écrit sur stdin est la moitié de la couture, et la seule que
// les fixtures ne couvrent pas.
func TestLaTrameDEntreeDitQuoiExecuter(t *testing.T) {
	entree := fauxRunner(t, "flux-succes.jsonl", "")

	cfg := config(t)
	cfg.Flags = []string{"--diff", "--limit=db-prod-02"}
	if _, _, err := lancer(t, cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}

	brut, err := os.ReadFile(entree)
	if err != nil {
		t.Fatalf("lecture de l'entrée capturée : %v", err)
	}

	lignes := strings.Split(strings.TrimSpace(string(brut)), "\n")
	if len(lignes) != 2 {
		t.Fatalf("entrée = %d lignes, attendu 2 (kwargs puis eof)\n%s", len(lignes), brut)
	}

	var trame struct {
		Kwargs struct {
			Playbook   string `json:"playbook"`
			ProjectDir string `json:"project_dir"`
			Inventory  string `json:"inventory"`
			Cmdline    string `json:"cmdline"`
		} `json:"kwargs"`
	}
	if err := json.Unmarshal([]byte(lignes[0]), &trame); err != nil {
		t.Fatalf("kwargs illisibles : %v", err)
	}
	if trame.Kwargs.Playbook != "site.yml" {
		t.Errorf("playbook = %q", trame.Kwargs.Playbook)
	}
	// L'arbre matérialisé sert directement de répertoire de projet : le
	// recopier sous le private_data_dir doublerait la taille du volume.
	if trame.Kwargs.ProjectDir != cfg.Workspace {
		t.Errorf("project_dir = %q, attendu %q", trame.Kwargs.ProjectDir, cfg.Workspace)
	}
	if trame.Kwargs.Inventory != filepath.Join(cfg.Workspace, "inv/staging") {
		t.Errorf("inventory = %q", trame.Kwargs.Inventory)
	}
	if trame.Kwargs.Cmdline != "--diff --limit=db-prod-02" {
		t.Errorf("cmdline = %q", trame.Kwargs.Cmdline)
	}

	// Sans `eof`, le worker attend une archive qui ne viendra jamais.
	var fin struct {
		EOF bool `json:"eof"`
	}
	if err := json.Unmarshal([]byte(lignes[1]), &fin); err != nil || !fin.EOF {
		t.Errorf("seconde ligne = %q, attendu la trame eof", lignes[1])
	}
}

// Un run AD-HOC porte `module`/`module_args`/`host_pattern` à la place de
// `playbook` — l'équivalent de `ansible <motif> -m <module> -a <args>`.
func TestLaTrameDEntreeDitQuoiExecuterEnAdhoc(t *testing.T) {
	entree := fauxRunner(t, "flux-succes.jsonl", "")

	base := t.TempDir()
	cfg := exec.Config{
		Workspace:      filepath.Join(base, "src"),
		PrivateDataDir: filepath.Join(base, "pdd"),
		Inventory:      "inv/staging",
		Module:         "shell",
		ModuleArgs:     "uptime",
		HostPattern:    "web",
	}
	// Pas de playbook à poser — un ad-hoc n'en a pas — mais l'inventaire, lui,
	// est bien un chemin de l'arbre audité, et le pilote le vérifie.
	poser(t, cfg.Workspace, cfg.Inventory, "[web]\nweb-01\n")
	if _, _, err := lancer(t, cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}

	brut, err := os.ReadFile(entree)
	if err != nil {
		t.Fatalf("lecture de l'entrée capturée : %v", err)
	}

	var trame struct {
		Kwargs struct {
			Playbook    string `json:"playbook"`
			Module      string `json:"module"`
			ModuleArgs  string `json:"module_args"`
			HostPattern string `json:"host_pattern"`
			ProjectDir  string `json:"project_dir"`
			Inventory   string `json:"inventory"`
		} `json:"kwargs"`
	}
	if err := json.Unmarshal([]byte(strings.SplitN(string(brut), "\n", 2)[0]), &trame); err != nil {
		t.Fatalf("kwargs illisibles : %v", err)
	}
	if trame.Kwargs.Playbook != "" {
		t.Errorf("playbook = %q, un run ad-hoc n'en a pas", trame.Kwargs.Playbook)
	}
	if trame.Kwargs.Module != "shell" {
		t.Errorf("module = %q", trame.Kwargs.Module)
	}
	if trame.Kwargs.ModuleArgs != "uptime" {
		t.Errorf("module_args = %q", trame.Kwargs.ModuleArgs)
	}
	if trame.Kwargs.HostPattern != "web" {
		t.Errorf("host_pattern = %q", trame.Kwargs.HostPattern)
	}
	if trame.Kwargs.ProjectDir != cfg.Workspace {
		t.Errorf("project_dir = %q, attendu %q", trame.Kwargs.ProjectDir, cfg.Workspace)
	}
}

// Sans arguments, `module_args` est omis plutôt qu'envoyé vide : beaucoup de
// modules de diagnostic (`ping`, `setup`) n'en attendent aucun.
func TestSansArgumentsModuleArgsEstOmis(t *testing.T) {
	entree := fauxRunner(t, "flux-succes.jsonl", "")

	base := t.TempDir()
	cfg := exec.Config{
		Workspace:      filepath.Join(base, "src"),
		PrivateDataDir: filepath.Join(base, "pdd"),
		Module:         "ping",
		HostPattern:    "all",
	}
	if _, _, err := lancer(t, cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}

	brut, err := os.ReadFile(entree)
	if err != nil {
		t.Fatalf("lecture : %v", err)
	}
	var trame struct {
		Kwargs map[string]any `json:"kwargs"`
	}
	if err := json.Unmarshal([]byte(strings.SplitN(string(brut), "\n", 2)[0]), &trame); err != nil {
		t.Fatalf("kwargs illisibles : %v", err)
	}
	if _, present := trame.Kwargs["module_args"]; present {
		t.Errorf("module_args présent alors qu'aucun argument n'a été donné : %v", trame.Kwargs)
	}
}

// `ansible-runner` découpe `cmdline` à la façon d'un shell. Les flags viennent
// pourtant du serveur sous forme de LISTE : les recoller sans citation rendrait
// à la valeur d'un `--extra-vars` le pouvoir d'en introduire d'autres.
func TestLesFlagsSontCitesEtNePeuventPasEnIntroduireDAutres(t *testing.T) {
	entree := fauxRunner(t, "flux-succes.jsonl", "")

	cfg := config(t)
	cfg.Flags = []string{`--extra-vars=msg=bonjour le monde --become`}
	if _, _, err := lancer(t, cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}

	brut, err := os.ReadFile(entree)
	if err != nil {
		t.Fatalf("lecture : %v", err)
	}
	var trame struct {
		Kwargs struct {
			Cmdline string `json:"cmdline"`
		} `json:"kwargs"`
	}
	if err := json.Unmarshal([]byte(strings.SplitN(string(brut), "\n", 2)[0]), &trame); err != nil {
		t.Fatalf("kwargs illisibles : %v", err)
	}
	// Un seul argument après découpage shell : la citation doit envelopper la
	// valeur entière, `--become` compris.
	if !strings.HasPrefix(trame.Kwargs.Cmdline, "'") || !strings.HasSuffix(trame.Kwargs.Cmdline, "'") {
		t.Errorf("cmdline = %q, la valeur n'est pas citée", trame.Kwargs.Cmdline)
	}
}

// Le playbook et l'inventaire viennent de l'ordre de run. Un `../` y ferait
// exécuter autre chose que le commit journalisé, ce que le §8.2 interdit.
func TestUnCheminQuiSortDeLArbreAuditeEstRefuse(t *testing.T) {
	base := t.TempDir()
	nu := exec.Config{
		Workspace:      filepath.Join(base, "src"),
		PrivateDataDir: filepath.Join(base, "pdd"),
	}

	cas := map[string]exec.Config{
		"playbook remontant": {Workspace: nu.Workspace, PrivateDataDir: nu.PrivateDataDir, Playbook: "../../etc/site.yml"},
		"playbook absolu":    {Workspace: nu.Workspace, PrivateDataDir: nu.PrivateDataDir, Playbook: "/etc/site.yml"},
		"inventaire remontant": {Workspace: nu.Workspace, PrivateDataDir: nu.PrivateDataDir,
			Playbook: "site.yml", Inventory: "../secrets/hosts"},
		"inventaire absolu": {Workspace: nu.Workspace, PrivateDataDir: nu.PrivateDataDir,
			Playbook: "site.yml", Inventory: "/etc/ansible/hosts"},
	}
	for nom, cfg := range cas {
		t.Run(nom, func(t *testing.T) {
			if _, err := exec.New(cfg); err == nil {
				t.Fatal("accepté à tort")
			}
		})
	}
}

func TestNewRefuseUnMontageIncomplet(t *testing.T) {
	cas := map[string]exec.Config{
		"sans espace de travail":     {PrivateDataDir: "/w/pdd", Playbook: "site.yml"},
		"sans playbook":              {Workspace: "/w/src", PrivateDataDir: "/w/pdd"},
		"sans répertoire de travail": {Workspace: "/w/src", Playbook: "site.yml"},

		// Ni l'un ni l'autre mode : rien à exécuter.
		"ni playbook ni ad-hoc": {Workspace: "/w/src", PrivateDataDir: "/w/pdd"},

		// Les deux modes à la fois : une confusion de la livraison de l'ordre,
		// pas une intention — ni New() ni kwargs() ne doivent trancher pour
		// l'utilisateur lequel des deux exécuter.
		"playbook et module à la fois": {
			Workspace: "/w/src", PrivateDataDir: "/w/pdd",
			Playbook: "site.yml", Module: "shell", HostPattern: "all",
		},

		// Ad-hoc incomplet : un module sans motif, ou l'inverse, ne dit pas
		// assez pour composer une trame `ansible-runner`.
		"module sans motif": {Workspace: "/w/src", PrivateDataDir: "/w/pdd", Module: "shell"},
		"motif sans module": {Workspace: "/w/src", PrivateDataDir: "/w/pdd", HostPattern: "all"},
	}
	for nom, cfg := range cas {
		t.Run(nom, func(t *testing.T) {
			if _, err := exec.New(cfg); err == nil {
				t.Fatal("accepté à tort")
			}
		})
	}
}

// Un run ad-hoc complet — module et motif renseignés, sans playbook — est
// accepté.
func TestNewAccepteUnAdhocComplet(t *testing.T) {
	cfg := exec.Config{
		Workspace: "/w/src", PrivateDataDir: "/w/pdd",
		Module: "shell", HostPattern: "all",
	}
	if _, err := exec.New(cfg); err != nil {
		t.Fatalf("un ad-hoc complet doit être accepté : %v", err)
	}
}

// L'annulation du §5.6 : un SIGINT, pas un SIGKILL. Le pilote doit rendre la
// main plutôt que d'attendre indéfiniment un processus qui ne lit plus.
func TestUneAnnulationInterromptLeRun(t *testing.T) {
	dir := t.TempDir()
	chemin := filepath.Join(dir, "ansible-runner")
	// Un runner qui n'émet rien et ne finit jamais de lui-même.
	script := "#!/bin/sh\ncat > /dev/null\nsleep 300\n"
	if err := os.WriteFile(chemin, []byte(script), 0o700); err != nil { // #nosec G306
		t.Fatalf("écriture : %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cfg := config(t)
	// Le sursis du §5.6 est raccourci : ce qu'on vérifie est que le pilote rend
	// la main, pas combien de temps il patiente.
	cfg.DelaiApresSignal = 200 * time.Millisecond
	r, err := exec.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, annuler := context.WithCancel(t.Context())
	fini := make(chan error, 1)
	go func() {
		_, err := r.Run(ctx, func(events.Event) {})
		fini <- err
	}()

	time.Sleep(100 * time.Millisecond)
	annuler()

	select {
	case err := <-fini:
		if err == nil {
			t.Error("un run annulé doit remonter une panne de pilotage")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("le pilote n'a pas rendu la main après annulation")
	}
}

// --- la clé d'accès (ADR-0020) ---

// Elle va dans `<pdd>/env/ssh_key`, et c'est MESURÉ : ansible-runner l'y trouve,
// démarre un ssh-agent, l'y charge et EFFACE le fichier avant de lancer le
// playbook. Écrire ailleurs — un fichier plus `--private-key` — la laisserait
// lisible par du code arbitraire pendant tout le run.
func TestLaCleDAccesEstPoseeLaOuAnsibleRunnerLaCherche(t *testing.T) {
	fauxRunner(t, "flux-succes.jsonl", "")

	cfg := config(t)
	const cle = "-----BEGIN OPENSSH PRIVATE KEY-----\nAAAA\n-----END OPENSSH PRIVATE KEY-----\n"
	cfg.SSHKey = []byte(cle)

	if _, _, err := lancer(t, cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}

	chemin := filepath.Join(cfg.PrivateDataDir, "env", "ssh_key")
	brut, err := os.ReadFile(chemin) // #nosec G304 -- chemin construit par le test
	if err != nil {
		t.Fatalf("la clé n'a pas été posée : %v", err)
	}
	if string(brut) != cle {
		t.Errorf("la clé a été réécrite : %q", brut)
	}

	// Le fichier existe le temps que le playbook démarre, et le playbook tourne
	// sous le même utilisateur : il ne doit pas être lisible plus largement que
	// nécessaire.
	fi, err := os.Stat(chemin)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("permissions = %v, attendu 0600", fi.Mode().Perm())
	}
	if rep, err := os.Stat(filepath.Dir(chemin)); err != nil {
		t.Fatalf("Stat du répertoire : %v", err)
	} else if rep.Mode().Perm() != 0o700 {
		t.Errorf("permissions du répertoire = %v, attendu 0700", rep.Mode().Perm())
	}
}

// Sans clé, RIEN n'est écrit. Un `env/ssh_key` vide ferait démarrer ssh-agent et
// échouer ssh-add, donc échouer le run entier — alors qu'un run sans clé doit
// simplement tourner sans clé.
func TestSansCleAucunFichierNEstPose(t *testing.T) {
	fauxRunner(t, "flux-succes.jsonl", "")

	cfg := config(t)
	if _, _, err := lancer(t, cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if _, err := os.Stat(filepath.Join(cfg.PrivateDataDir, "env", "ssh_key")); err == nil {
		t.Error("un fichier de clé a été posé alors qu'il n'y a pas de clé")
	}
}

// --- le mot de passe d'élévation (§5.5) ---

// Il va dans `<pdd>/env/extravars`, et le chemin a été MESURÉ contre les deux
// autres : `passwords` en kwargs fait basculer ansible-runner sous pexpect et le
// flux perd sa structure — plus de champ `event`, plus de `job_events`, l'audit
// disparaît en silence. `-e ansible_become_password=…` le mettrait sur argv.
func TestLeMotDePasseDElevationVaDansExtravars(t *testing.T) {
	fauxRunner(t, "flux-succes.jsonl", "")

	cfg := config(t)
	cfg.BecomePassword = []byte("hunter2")
	if _, _, err := lancer(t, cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}

	chemin := filepath.Join(cfg.PrivateDataDir, "env", "extravars")
	brut, err := os.ReadFile(chemin) // #nosec G304 -- chemin construit par le test
	if err != nil {
		t.Fatalf("le mot de passe n'a pas été posé : %v", err)
	}

	var vars map[string]string
	if err := json.Unmarshal(brut, &vars); err != nil {
		t.Fatalf("extravars illisibles : %v", err)
	}
	if vars["ansible_become_password"] != "hunter2" {
		t.Errorf("extravars = %v", vars)
	}

	fi, err := os.Stat(chemin)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("permissions = %v, attendu 0600", fi.Mode().Perm())
	}
}

// Un mot de passe contenant des caractères que YAML ou un shell interpréteraient
// doit traverser INTACT : `encoding/json` échappe ce qu'il faut, et le
// contourner produirait un fichier illisible dont l'erreur parlerait de YAML.
func TestUnMotDePasseAvecDesCaracteresDelicatsTraverse(t *testing.T) {
	fauxRunner(t, "flux-succes.jsonl", "")

	const delicat = "gui\"llemet\nsaut: deux-points #dièse\\antislash"
	cfg := config(t)
	cfg.BecomePassword = []byte(delicat)
	if _, _, err := lancer(t, cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}

	brut, err := os.ReadFile(filepath.Join(cfg.PrivateDataDir, "env", "extravars")) // #nosec G304
	if err != nil {
		t.Fatalf("lecture : %v", err)
	}
	var vars map[string]string
	if err := json.Unmarshal(brut, &vars); err != nil {
		t.Fatalf("extravars illisibles : %v", err)
	}
	if vars["ansible_become_password"] != delicat {
		t.Errorf("mot de passe altéré : %q", vars["ansible_become_password"])
	}
}

// Sans mot de passe, AUCUN fichier n'est posé : un `extravars` vide ferait
// passer un `-e @fichier` inutile à chaque run.
func TestSansMotDePasseDElevationAucunExtravars(t *testing.T) {
	fauxRunner(t, "flux-succes.jsonl", "")

	cfg := config(t)
	if _, _, err := lancer(t, cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.PrivateDataDir, "env", "extravars")); err == nil {
		t.Error("un extravars a été posé alors qu'il n'y a pas de mot de passe")
	}
}

// --- la clé de vault (ADR-0045) ---

// Elle va dans `<pdd>/env/vault_password`, et `--vault-password-file` est
// ajouté à `cmdline` SANS passer par `Flags` — c'est le point que
// TestLesFlagsSontCitesEtNePeuventPasEnIntroduireDAutres ne couvre pas : ici
// on vérifie que l'argument apparaît bien, construit par ce paquet et non par
// l'allowlist serveur.
func TestLaCleDeVaultEstPoseeEtReferenceeSurLaLigneDeCommande(t *testing.T) {
	entree := fauxRunner(t, "flux-succes.jsonl", "")

	cfg := config(t)
	const cle = "un-mot-de-passe-de-vault"
	cfg.VaultPassword = []byte(cle)

	if _, _, err := lancer(t, cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}

	chemin := filepath.Join(cfg.PrivateDataDir, "env", "vault_password")
	brut, err := os.ReadFile(chemin) // #nosec G304 -- chemin construit par le test
	if err != nil {
		t.Fatalf("la clé de vault n'a pas été posée : %v", err)
	}
	if string(brut) != cle {
		t.Errorf("la clé de vault a été réécrite : %q", brut)
	}
	if fi, err := os.Stat(chemin); err != nil {
		t.Fatalf("Stat: %v", err)
	} else if fi.Mode().Perm() != 0o600 {
		t.Errorf("permissions = %v, attendu 0600", fi.Mode().Perm())
	}

	entreeBrute, err := os.ReadFile(entree)
	if err != nil {
		t.Fatalf("lecture : %v", err)
	}
	var trame struct {
		Kwargs struct {
			Cmdline string `json:"cmdline"`
		} `json:"kwargs"`
	}
	if err := json.Unmarshal([]byte(strings.SplitN(string(entreeBrute), "\n", 2)[0]), &trame); err != nil {
		t.Fatalf("kwargs illisibles : %v", err)
	}
	if !strings.Contains(trame.Kwargs.Cmdline, "--vault-password-file") || !strings.Contains(trame.Kwargs.Cmdline, chemin) {
		t.Errorf("cmdline = %q, attendu --vault-password-file %s", trame.Kwargs.Cmdline, chemin)
	}
}

// Sans clé de vault, RIEN n'est écrit et `cmdline` ne porte aucun
// `--vault-password-file` — un run sans secret `!vault |` doit tourner sans
// qu'Ansible cherche un fichier qui n'existe pas.
func TestSansCleDeVaultAucunFichierNiArgument(t *testing.T) {
	entree := fauxRunner(t, "flux-succes.jsonl", "")

	cfg := config(t)
	if _, _, err := lancer(t, cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if _, err := os.Stat(filepath.Join(cfg.PrivateDataDir, "env", "vault_password")); err == nil {
		t.Error("un fichier de clé de vault a été posé alors qu'il n'y a pas de clé")
	}

	brut, err := os.ReadFile(entree)
	if err != nil {
		t.Fatalf("lecture : %v", err)
	}
	if strings.Contains(string(brut), "vault-password-file") {
		t.Error("--vault-password-file présent alors qu'il n'y a pas de clé de vault")
	}
}

// Les deux — flags utilisateur ET clé de vault — coexistent sur la même
// ligne, chacun cité, et l'un ne doit pas pouvoir s'introduire dans l'autre.
func TestLaCleDeVaultEtLesFlagsUtilisateurCoexistent(t *testing.T) {
	entree := fauxRunner(t, "flux-succes.jsonl", "")

	cfg := config(t)
	cfg.VaultPassword = []byte("un-mot-de-passe")
	cfg.Flags = []string{"--check", "--diff"}

	if _, _, err := lancer(t, cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}

	brut, err := os.ReadFile(entree)
	if err != nil {
		t.Fatalf("lecture : %v", err)
	}
	var trame struct {
		Kwargs struct {
			Cmdline string `json:"cmdline"`
		} `json:"kwargs"`
	}
	if err := json.Unmarshal([]byte(strings.SplitN(string(brut), "\n", 2)[0]), &trame); err != nil {
		t.Fatalf("kwargs illisibles : %v", err)
	}
	for _, attendu := range []string{"--vault-password-file", "--check", "--diff"} {
		if !strings.Contains(trame.Kwargs.Cmdline, attendu) {
			t.Errorf("cmdline = %q, attendu %q", trame.Kwargs.Cmdline, attendu)
		}
	}
}

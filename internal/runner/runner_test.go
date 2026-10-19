// Test interne : `lireJeton` n'est pas exportée, et ce qu'elle porte — une
// contrainte de chemin — est un contrôle de sécurité qui mérite d'être vérifié
// plutôt que relu.

package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	wire "github.com/jijins1/warden/internal/wire/dispatch"
)

// TestLeCheminDuJetonEstContraintAuMontage est la propriété qui compte.
//
// Ce binaire tourne dans le MÊME conteneur que le playbook : tout ce qui vient de
// son environnement est, à terme, influençable par du code arbitraire (§2). Lire
// un chemin quelconque ferait de cette fonction une primitive de lecture de
// fichier au service de qui contrôle l'environnement — utilisable, par exemple,
// pour faire remonter le contenu d'un fichier dans un message d'erreur.
func TestLeCheminDuJetonEstContraintAuMontage(t *testing.T) {
	// Un fichier bien réel, hors du montage : le refus ne doit pas dépendre de
	// son absence.
	hors := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(hors, []byte("contenu qui ne doit pas sortir"), 0o600); err != nil {
		t.Fatalf("écriture : %v", err)
	}

	cas := []string{
		hors,
		"/etc/passwd",
		"/proc/self/environ",
		MountDir + "/../../etc/passwd",
		MountDir, // le répertoire lui-même, pas un fichier dedans
		"",
	}

	for _, chemin := range cas {
		t.Run(chemin, func(t *testing.T) {
			jeton, err := lireJeton(chemin, strings.NewReader("ne doit pas être lu"))
			if err == nil {
				t.Fatalf("chemin accepté, jeton lu : %q", jeton)
			}
			// Et le contenu du fichier visé ne fuite pas dans l'erreur.
			if strings.Contains(err.Error(), "ne doit pas sortir") {
				t.Errorf("le contenu du fichier fuite dans l'erreur : %v", err)
			}
		})
	}
}

// TestUnJetonVideEstRefuse : un runner sans jeton ne peut rien faire, et le
// laisser continuer produirait un échec plus loin, avec un message parlant
// d'autre chose.
func TestUnJetonVideEstRefuse(t *testing.T) {
	// Le montage réel n'existe pas sur la machine de test ; on vérifie donc que
	// la contrainte s'applique AVANT la lecture, ce qui est justement l'ordre
	// voulu.
	if _, err := lireJeton(MountDir+"/token", strings.NewReader("")); err == nil {
		t.Fatal("un jeton absent a été accepté")
	}
}

// TestLeJetonSeLitSurStdin : le mode Docker (ADR-0047) passe le jeton par
// stdin, qui est fermé après l'écriture.
func TestLeJetonSeLitSurStdin(t *testing.T) {
	jeton, err := lireJeton(wire.TokenFileStdin, strings.NewReader("jeton-amorcage\n"))
	if err != nil {
		t.Fatalf("lecture sur stdin : %v", err)
	}
	if jeton != "jeton-amorcage" {
		t.Errorf("jeton lu : %q", jeton)
	}
}

// TestUnStdinVideEstRefuse : un serveur qui ferme stdin sans rien écrire laisse
// le runner sans identité.
func TestUnStdinVideEstRefuse(t *testing.T) {
	if _, err := lireJeton(wire.TokenFileStdin, strings.NewReader(" \n")); err == nil {
		t.Fatal("un stdin vide a été accepté")
	}
}

// TestUnStdinSansFinEstBorne : un flux qui ne s'arrête pas ne doit pas faire
// grossir le runner sans limite.
func TestUnStdinSansFinEstBorne(t *testing.T) {
	trop := strings.NewReader(strings.Repeat("x", tailleMaxJeton+1))
	if _, err := lireJeton(wire.TokenFileStdin, trop); err == nil {
		t.Fatal("un jeton démesuré a été accepté")
	}
}

// TestLeRunnerNaccepteAucunArgument.
//
// La ligne de commande d'un pod est visible et modifiable par qui édite la
// podspec. Tout ce que le runner doit savoir vient du canal de dispatch, dont
// l'authenticité est établie par son jeton — pas d'argv.
func TestLeRunnerNaccepteAucunArgument(t *testing.T) {
	err := Run(t.Context(), []string{"--playbook", "site.yml"})
	if err == nil {
		t.Fatal("des arguments ont été acceptés")
	}
	if !strings.Contains(err.Error(), "aucun argument") {
		t.Errorf("refusé pour une autre raison : %v", err)
	}
}

// LE test du branchement de l'ADR-0017. Sans lui, les valeurs transmises
// ressortiraient EN CLAIR dans les events remontés — le rédacteur était `nil`
// jusqu'ici, et rien ne l'aurait signalé.
func TestRedacteurMasqueLesValeursInjectees(t *testing.T) {
	red := redacteurPour(wire.Order{
		ForwardEnv: map[string]wire.Secret{
			"VAULT_TOKEN": wire.Secret("hvs.SUPERSECRET123"),
		},
		BecomePassword: wire.Secret("motdepasse-eleve"),
	})
	if red == nil {
		t.Fatal("un ordre portant des valeurs injectées doit armer le rédacteur")
	}

	sortie, _, _ := red.RedactText(
		"le mot de passe est motdepasse-eleve et le jeton hvs.SUPERSECRET123")
	if strings.Contains(sortie, "hvs.SUPERSECRET123") {
		t.Errorf("la valeur transmise ressort en clair : %q", sortie)
	}
	if strings.Contains(sortie, "motdepasse-eleve") {
		t.Errorf("le mot de passe d'élévation ressort en clair : %q", sortie)
	}
}

// La clé d'accès N'ENTRE PAS dans le jeu à masquer (ADR-0020) : elle vient du
// serveur et n'apparaît jamais dans un résultat de module. L'ajouter ferait
// masquer des fragments de diff sans rapport.
func TestRedacteurIgnoreLaCleDAcces(t *testing.T) {
	if red := redacteurPour(wire.Order{SSHKey: wire.Secret("-----BEGIN KEY-----")}); red != nil {
		t.Error("une clé d'accès seule ne doit pas armer le rédacteur")
	}
}

// La clé de vault, elle, EST du jeu (ADR-0045) : contrairement à SSHKey, elle
// vit dans un fichier lisible par le playbook pendant toute la durée du run,
// et un module qui la recopierait dans sa sortie doit la voir masquée.
func TestRedacteurMasqueLaCleDeVault(t *testing.T) {
	red := redacteurPour(wire.Order{VaultPassword: wire.Secret("mot-de-passe-de-vault")})
	if red == nil {
		t.Fatal("un ordre portant une clé de vault doit armer le rédacteur")
	}

	sortie, _, _ := red.RedactText("la clé est mot-de-passe-de-vault")
	if strings.Contains(sortie, "mot-de-passe-de-vault") {
		t.Errorf("la clé de vault ressort en clair : %q", sortie)
	}
}

// Rien à masquer, pas de rédacteur : le faire parcourir chaque ligne du flux
// pour zéro aiguille serait payé sur le chemin chaud.
func TestRedacteurNilQuandRienNEstInjecte(t *testing.T) {
	if red := redacteurPour(wire.Order{RunRef: "x"}); red != nil {
		t.Error("un ordre sans valeur injectée ne doit pas armer le rédacteur")
	}
}

// enClair sort les valeurs de leur enveloppe, et rend nil quand il n'y a rien —
// ce que `poserLesVariables` distingue pour ne pas écrire de fichier vide.
func TestEnClairRendNilSurVide(t *testing.T) {
	if enClair(nil) != nil || enClair(map[string]wire.Secret{}) != nil {
		t.Error("enClair doit rendre nil plutôt qu'une map vide")
	}
}

// Le mot de passe de connexion EST du jeu (ADR-0048) : il vit dans le même
// fichier que celui d'élévation, lisible par le playbook.
func TestRedacteurMasqueLeMotDePasseDeConnexion(t *testing.T) {
	red := redacteurPour(wire.Order{ConnectionPassword: wire.Secret("mdp-ldap-alice")})
	if red == nil {
		t.Fatal("un ordre portant un mot de passe de connexion doit armer le rédacteur")
	}
	sortie, _, _ := red.RedactText("le mot de passe est mdp-ldap-alice")
	if strings.Contains(sortie, "mdp-ldap-alice") {
		t.Errorf("le mot de passe de connexion ressort en clair : %q", sortie)
	}
}

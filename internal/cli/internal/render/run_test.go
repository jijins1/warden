package render_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jijins1/warden/internal/cli/internal/render"
	"github.com/jijins1/warden/internal/cli/internal/stream"
	"github.com/jijins1/warden/internal/events"
	"github.com/jijins1/warden/internal/wire/ws"
)

// suiveurFactice rejoue une suite d'events, sans WebSocket ni serveur.
type suiveurFactice struct {
	frames []ws.EventFrame
	fin    stream.Fin
	err    error
	vus    []string
}

func (s *suiveurFactice) Suivre(_ context.Context, runRef string, voir func(ws.EventFrame)) (stream.Fin, error) {
	s.vus = append(s.vus, runRef)
	for _, f := range s.frames {
		voir(f)
	}
	return s.fin, s.err
}

func frame(seq int64, tache int, role, nom, hote string, etat events.State) ws.EventFrame {
	return ws.EventFrame{
		Seq: seq, TaskNumber: tache, Role: role, TaskName: nom,
		Host: hote, State: etat, Payload: json.RawMessage(`{}`),
	}
}

// Le titre de tâche n'est écrit QU'UNE FOIS, quel que soit le nombre de machines
// qui la traversent. C'est ce qui rend la sortie lisible sur une flotte.
func TestUneTacheNestAnnonceeQuUneFois(t *testing.T) {
	s := &suiveurFactice{
		frames: []ws.EventFrame{
			frame(0, 1, "", "Gathering Facts", "kodi", events.StateOK),
			frame(1, 1, "", "Gathering Facts", "muzi", events.StateOK),
			frame(2, 2, "node_exporter", "installer le binaire", "kodi", events.StateChanged),
			frame(3, 2, "node_exporter", "installer le binaire", "muzi", events.StateOK),
		},
		fin: stream.Fin{Status: render.IssueReussi},
	}

	var out strings.Builder
	if _, err := render.Run(t.Context(), s, "01JZQ", &out); err != nil {
		t.Fatalf("Run : %v", err)
	}

	if n := strings.Count(out.String(), "TASK [Gathering Facts]"); n != 1 {
		t.Errorf("titre écrit %d fois, voulu 1\n%s", n, out.String())
	}
	// Le rôle préfixe le nom : deux rôles portant « installer le paquet »
	// seraient sinon indiscernables au moment où l'on cherche lequel a échoué.
	if !strings.Contains(out.String(), "TASK [node_exporter : installer le binaire]") {
		t.Errorf("le rôle ne préfixe pas la tâche :\n%s", out.String())
	}
}

// `unreachable` ne se confond pas avec `failed` : une machine injoignable est un
// problème de réseau ou de clé, pas de playbook. Les deux restent nommées par
// hôte même dans le résumé replié — c'est l'information qu'on ne doit jamais
// avoir à aller chercher.
func TestLesEtatsOntDesMarqueursDistincts(t *testing.T) {
	s := &suiveurFactice{
		frames: []ws.EventFrame{
			frame(0, 1, "", "t", "a", events.StateOK),
			frame(1, 1, "", "t", "b", events.StateChanged),
			frame(2, 1, "", "t", "c", events.StateFailed),
			frame(3, 1, "", "t", "d", events.StateUnreachable),
			frame(4, 1, "", "t", "e", events.StateSkipped),
		},
		fin: stream.Fin{Status: render.IssueEchec},
	}

	var out strings.Builder
	if _, err := render.Run(t.Context(), s, "01JZQ", &out); err != nil {
		t.Fatalf("Run : %v", err)
	}
	// Résumé de la tâche : les états sans échec sont COMPTÉS, pas listés par hôte.
	for _, veut := range []string{"ok=1", "changed=1", "skipped=1"} {
		if !strings.Contains(out.String(), veut) {
			t.Errorf("résumé sans %q :\n%s", veut, out.String())
		}
	}
	// Les échecs, eux, restent nommés — jamais repliés dans un simple compte.
	for _, veut := range []string{"✗ c", "⚠ d"} {
		if !strings.Contains(out.String(), veut) {
			t.Errorf("sortie sans %q :\n%s", veut, out.String())
		}
	}
}

// Un event sans hôte — le play lui-même, pas une machine — ne doit produire
// AUCUNE ligne : c'était le bruit qui noyait les tâches sur un run bavard.
func TestUnEventSansHoteNeProduitAucuneLigne(t *testing.T) {
	s := &suiveurFactice{
		frames: []ws.EventFrame{
			frame(0, 0, "", "", "", events.StateUnknown),
			frame(1, 1, "", "t", "kodi", events.StateOK),
		},
		fin: stream.Fin{Status: render.IssueReussi},
	}

	var out strings.Builder
	if _, err := render.Run(t.Context(), s, "01JZQ", &out); err != nil {
		t.Fatalf("Run : %v", err)
	}
	if strings.Contains(out.String(), "—") {
		t.Errorf("un event sans hôte a laissé une ligne :\n%s", out.String())
	}
}

// Un run qui échoue sans qu'AUCUNE tâche ne démarre doit quand même dire
// pourquoi.
//
// C'est le défaut constaté en production : un playbook introuvable ne produit
// que des events sans hôte, tous écartés par le filtre ci-dessus, et la sortie
// se réduisait à `run <ref> : failed`. Le diagnostic écrit par warden passe
// désormais, et lui seul — les trames `verbose` d'Ansible restent du bruit.
func TestUnEchecSansTacheDitPourquoi(t *testing.T) {
	diagnostic := frame(0, 0, "", "", "", events.StateFailed)
	diagnostic.Payload = json.RawMessage(
		`{"` + events.CleErreur + `":"playbook introuvable dans le commit audité : site.yml"}`)

	s := &suiveurFactice{
		frames: []ws.EventFrame{diagnostic},
		fin:    stream.Fin{Status: render.IssueEchec},
	}

	var out strings.Builder
	if _, err := render.Run(t.Context(), s, "01JZQ", &out); err != nil {
		t.Fatalf("Run : %v", err)
	}
	if !strings.Contains(out.String(), "site.yml") {
		t.Errorf("le motif de l'échec n'apparaît pas :\n%s", out.String())
	}
}

// Le bilan est trié : l'ordre d'itération d'une map varie d'un run à l'autre, et
// un récapitulatif dont les lignes bougent sans que rien n'ait changé se relit
// mal.
func TestLeBilanEstTrieEtCompte(t *testing.T) {
	s := &suiveurFactice{
		frames: []ws.EventFrame{
			frame(0, 1, "", "t", "muzi", events.StateChanged),
			frame(1, 1, "", "t", "kodi", events.StateOK),
			frame(2, 2, "", "u", "muzi", events.StateChanged),
			frame(3, 2, "", "u", "kodi", events.StateOK),
		},
		fin: stream.Fin{Status: render.IssueReussi},
	}

	var out strings.Builder
	if _, err := render.Run(t.Context(), s, "01JZQ", &out); err != nil {
		t.Fatalf("Run : %v", err)
	}

	bilan := out.String()[strings.Index(out.String(), "Bilan"):]
	if strings.Index(bilan, "kodi") > strings.Index(bilan, "muzi") {
		t.Errorf("bilan non trié :\n%s", bilan)
	}
	if !strings.Contains(bilan, "changed=2") || !strings.Contains(bilan, "ok=2") {
		t.Errorf("comptes faux :\n%s", bilan)
	}
}

// Une coupure après huit minutes de run ne doit pas emporter ce qu'on a déjà vu
// passer : le bilan est écrit MÊME sur erreur.
func TestUneCoupureNemportePasLeBilan(t *testing.T) {
	s := &suiveurFactice{
		frames: []ws.EventFrame{frame(0, 1, "", "t", "kodi", events.StateChanged)},
		err:    context.Canceled,
	}

	var out strings.Builder
	if _, err := render.Run(t.Context(), s, "01JZQ", &out); err == nil {
		t.Fatal("erreur avalée")
	}
	if !strings.Contains(out.String(), "kodi") {
		t.Errorf("le bilan a disparu avec la coupure :\n%s", out.String())
	}
}

// C'est `Issue` qui décide du code de sortie, et c'est LA propriété qui rend
// `warden submit` utilisable dans un pipeline.
func TestIssueDitSiCestUnEchec(t *testing.T) {
	cas := []struct {
		nom    string
		fin    stream.Fin
		echec  bool
		contie string
	}{
		{"réussi", stream.Fin{Status: render.IssueReussi}, false, "réussi"},
		{"échec", stream.Fin{Status: render.IssueEchec}, true, "failed"},
		// La cause est TOUJOURS renseignée sur un run interrompu (§6.4).
		{"interrompu", stream.Fin{Status: render.IssueInterrompu, Reason: "cancelled"}, true, "cancelled"},
		// Un statut inconnu est un ÉCHEC : supposer la réussite serait le mode
		// de défaillance le plus coûteux d'un produit d'audit.
		{"statut inconnu", stream.Fin{Status: "zarbi"}, true, "zarbi"},
	}
	for _, c := range cas {
		t.Run(c.nom, func(t *testing.T) {
			var out strings.Builder
			if got := render.Issue(&out, "01JZQ", c.fin); got != c.echec {
				t.Errorf("Issue = %v, veut %v", got, c.echec)
			}
			if !strings.Contains(out.String(), c.contie) {
				t.Errorf("sortie %q sans %q", out.String(), c.contie)
			}
		})
	}
}

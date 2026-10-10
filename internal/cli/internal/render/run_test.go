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

// trame compose un event tel que le flux le livre : le payload est la ligne
// d'`ansible-runner`, dont `stdout` porte le texte qu'Ansible a rendu.
func trame(seq int64, hote string, etat events.State, evt, stdout string) ws.EventFrame {
	charge, _ := json.Marshal(map[string]string{"event": evt, "stdout": stdout})
	return ws.EventFrame{Seq: seq, Host: hote, State: etat, Payload: charge}
}

// Les textes ci-dessous sont MESURÉS : ansible-runner 2.4.3 sous
// `ANSIBLE_FORCE_COLOR=1`, playbook à quatre tâches, `--diff`.
const (
	vert  = "\x1b[0;32m"
	jaune = "\x1b[0;33m"
	cyan  = "\x1b[0;36m"
	fin   = "\x1b[0m"
)

func runMesure() []ws.EventFrame {
	return []ws.EventFrame{
		trame(0, "", events.StateUnknown, "playbook_on_start", ""),
		trame(1, "", events.StateUnknown, "playbook_on_play_start",
			"\r\nPLAY [localhost] ***************************************************************"),
		trame(2, "", events.StateUnknown, "playbook_on_task_start",
			"\r\nTASK [command] *****************************************************************"),
		trame(3, "localhost", events.StateUnknown, "runner_on_start", ""),
		trame(4, "localhost", events.StateOK, "runner_on_ok", vert+"ok: [localhost]"+fin),
		trame(5, "", events.StateUnknown, "playbook_on_task_start",
			"\r\nTASK [debug] *******************************************************************"),
		trame(6, "localhost", events.StateSkipped, "runner_on_skipped", cyan+"skipping: [localhost]"+fin),
		trame(7, "", events.StateUnknown, "playbook_on_stats",
			"\r\nPLAY RECAP *********************************************************************\r\n"+
				jaune+"localhost"+fin+"                  : "+vert+"ok=1   "+fin+" changed=0    unreachable=0    failed=0    "+
				cyan+"skipped=1   "+fin+" rescued=0    ignored=0   "),
	}
}

// LA propriété : ce que la CLI écrit est, au caractère près, ce
// qu'`ansible-playbook` aurait écrit. Ni résumé, ni réordonnancement.
func TestLaSortieEstCelleDAnsible(t *testing.T) {
	s := &suiveurFactice{frames: runMesure(), fin: stream.Fin{Status: render.IssueReussi}}

	var out strings.Builder
	if _, err := render.Run(t.Context(), s, "01JZQ", &out, false); err != nil {
		t.Fatalf("Run : %v", err)
	}

	veut := `
PLAY [localhost] ***************************************************************

TASK [command] *****************************************************************
ok: [localhost]

TASK [debug] *******************************************************************
skipping: [localhost]

PLAY RECAP *********************************************************************
localhost                  : ok=1    changed=0    unreachable=0    failed=0    skipped=1    rescued=0    ignored=0   
`
	if out.String() != veut {
		t.Errorf("sortie :\n%q\nveut :\n%q", out.String(), veut)
	}
}

// Sur un terminal, les couleurs sont celles qu'Ansible a choisies — transmises,
// pas réinventées.
func TestLesCouleursDAnsibleSontGardeesSurUnTerminal(t *testing.T) {
	s := &suiveurFactice{frames: runMesure(), fin: stream.Fin{Status: render.IssueReussi}}

	var out strings.Builder
	if _, err := render.Run(t.Context(), s, "01JZQ", &out, true); err != nil {
		t.Fatalf("Run : %v", err)
	}
	for _, veut := range []string{vert + "ok: [localhost]" + fin, cyan + "skipping: [localhost]" + fin} {
		if !strings.Contains(out.String(), veut) {
			t.Errorf("sortie sans %q :\n%q", veut, out.String())
		}
	}
	if strings.Contains(out.String(), "\r") {
		t.Errorf("retour chariot laissé dans la sortie :\n%q", out.String())
	}
}

// Le runner retire le texte rendu des events porteurs d'un diff, parce que ce
// texte EST le diff. La ligne d'état qu'Ansible aurait écrite est reconstituée,
// dans sa couleur ; le diff, lui, n'est jamais montré ici (§6.5).
func TestUnEventPriveDeSonTexteRetombeSurLaLigneDAnsible(t *testing.T) {
	sansTexte := func(seq int64, hote string, etat events.State, evt string) ws.EventFrame {
		charge, _ := json.Marshal(map[string]string{"event": evt})
		return ws.EventFrame{Seq: seq, Host: hote, State: etat, Payload: charge}
	}
	s := &suiveurFactice{
		frames: []ws.EventFrame{
			sansTexte(0, "kodi", events.StateUnknown, "runner_on_file_diff"),
			sansTexte(1, "kodi", events.StateChanged, "runner_on_ok"),
		},
		fin: stream.Fin{Status: render.IssueReussi},
	}

	var out strings.Builder
	if _, err := render.Run(t.Context(), s, "01JZQ", &out, true); err != nil {
		t.Fatalf("Run : %v", err)
	}
	// Une seule ligne : l'event de diff, sans texte ni état, n'en produit pas.
	if !strings.HasPrefix(out.String(), jaune+"changed: [kodi]"+fin+"\n") {
		t.Errorf("sortie :\n%q", out.String())
	}
}

// Un run qui échoue sans qu'AUCUNE tâche ne démarre doit quand même dire
// pourquoi — à la manière d'Ansible, qui préfixe ses erreurs fatales.
func TestUnEchecSansTacheDitPourquoi(t *testing.T) {
	diagnostic := ws.EventFrame{State: events.StateFailed, Payload: json.RawMessage(
		`{"` + events.CleErreur + `":"playbook introuvable dans le commit audité : site.yml"}`)}

	s := &suiveurFactice{
		frames: []ws.EventFrame{diagnostic},
		fin:    stream.Fin{Status: render.IssueEchec},
	}

	var out strings.Builder
	if _, err := render.Run(t.Context(), s, "01JZQ", &out, false); err != nil {
		t.Fatalf("Run : %v", err)
	}
	if !strings.Contains(out.String(), "ERROR! playbook introuvable dans le commit audité : site.yml") {
		t.Errorf("le motif de l'échec n'apparaît pas :\n%s", out.String())
	}
}

// Quand Ansible a expliqué l'échec lui-même — erreur de syntaxe —, son texte
// est déjà affiché tel qu'il l'a écrit. Le diagnostic, qui n'en est que la
// concaténation, le répéterait.
func TestLeDiagnosticNeRepetePasAnsible(t *testing.T) {
	diagnostic := ws.EventFrame{State: events.StateFailed, Payload: json.RawMessage(
		`{"` + events.CleErreur + `":"ERROR! A malformed block was encountered"}`)}

	s := &suiveurFactice{
		frames: []ws.EventFrame{
			trame(0, "", events.StateUnknown, "verbose", "\x1b[0;31mERROR! A malformed block was encountered\x1b[0m"),
			diagnostic,
		},
		fin: stream.Fin{Status: render.IssueEchec},
	}

	var out strings.Builder
	if _, err := render.Run(t.Context(), s, "01JZQ", &out, false); err != nil {
		t.Fatalf("Run : %v", err)
	}
	if out.String() != "ERROR! A malformed block was encountered\n" {
		t.Errorf("sortie :\n%q", out.String())
	}
}

// Une coupure après huit minutes de run ne doit pas emporter ce qu'on a déjà vu
// passer : sans le PLAY RECAP d'Ansible, la CLI en écrit un, au même format,
// à partir de ce qu'elle a compté.
func TestUneCoupureNemportePasLeBilan(t *testing.T) {
	s := &suiveurFactice{
		frames: []ws.EventFrame{
			trame(0, "muzi", events.StateChanged, "runner_on_ok", jaune+"changed: [muzi]"+fin),
			trame(1, "kodi", events.StateOK, "runner_on_ok", vert+"ok: [kodi]"+fin),
			trame(2, "kodi", events.StateFailed, "runner_on_failed", "fatal: [kodi]: FAILED! => {}"),
		},
		err: context.Canceled,
	}

	var out strings.Builder
	if _, err := render.Run(t.Context(), s, "01JZQ", &out, false); err == nil {
		t.Fatal("erreur avalée")
	}
	veut := `
PLAY RECAP *********************************************************************
kodi                       : ok=1    changed=0    unreachable=0    failed=1    skipped=0    rescued=0    ignored=0   
muzi                       : ok=1    changed=1    unreachable=0    failed=0    skipped=0    rescued=0    ignored=0   
`
	if !strings.HasSuffix(out.String(), veut) {
		t.Errorf("bilan :\n%q\nveut le suffixe :\n%q", out.String(), veut)
	}
}

// Quand Ansible a écrit son PLAY RECAP, la CLI n'en ajoute pas un second.
func TestLeBilanDAnsibleNEstPasDouble(t *testing.T) {
	s := &suiveurFactice{frames: runMesure(), err: context.Canceled}

	var out strings.Builder
	_, _ = render.Run(t.Context(), s, "01JZQ", &out, false)
	if n := strings.Count(out.String(), "PLAY RECAP"); n != 1 {
		t.Errorf("PLAY RECAP écrit %d fois :\n%s", n, out.String())
	}
}

// Les règles sont celles d'Ansible (`ansible/utils/color.py`) : NOCOLOR coupe,
// une sortie qui n'est pas un terminal coupe, et FORCE_COLOR l'emporte sur tout.
func TestLaCouleurSuitLesReglesDAnsible(t *testing.T) {
	cas := []struct {
		nom      string
		env      map[string]string
		terminal bool
		veut     bool
	}{
		{"terminal", nil, true, true},
		{"tube", nil, false, false},
		{"NO_COLOR", map[string]string{"NO_COLOR": "1"}, true, false},
		{"ANSIBLE_NOCOLOR", map[string]string{"ANSIBLE_NOCOLOR": "true"}, true, false},
		{"ANSIBLE_NOCOLOR faux", map[string]string{"ANSIBLE_NOCOLOR": "0"}, true, true},
		{"FORCE_COLOR sur un tube", map[string]string{"ANSIBLE_FORCE_COLOR": "yes"}, false, true},
		{"FORCE_COLOR l'emporte", map[string]string{"ANSIBLE_FORCE_COLOR": "1", "NO_COLOR": "1"}, false, true},
	}
	for _, c := range cas {
		t.Run(c.nom, func(t *testing.T) {
			getenv := func(k string) string { return c.env[k] }
			if got := render.CouleurPour(getenv, c.terminal); got != c.veut {
				t.Errorf("CouleurPour = %v, veut %v", got, c.veut)
			}
		})
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

// Le texte vient du pod runner, domaine hostile (ADR-0011) : un playbook y
// écrit ce qu'il veut. Seules les COULEURS passent ; effacer l'écran, changer
// le titre ou écrire dans le presse-papiers (OSC 52) ne passe pas.
func TestSeulesLesCouleursAtteignentLeTerminal(t *testing.T) {
	hostile := vert + "ok: [kodi]" + fin +
		"\x1b[2J\x1b]0;titre\x07\x1b]52;c;cGF5bG9hZA==\x07\u009b2J\x08\r fin"
	s := &suiveurFactice{
		frames: []ws.EventFrame{trame(0, "", events.StateUnknown, "verbose", hostile)},
		fin:    stream.Fin{Status: render.IssueReussi},
	}

	var out strings.Builder
	if _, err := render.Run(t.Context(), s, "01JZQ", &out, true); err != nil {
		t.Fatalf("Run : %v", err)
	}
	propre := strings.ReplaceAll(strings.ReplaceAll(out.String(), vert, ""), fin, "")
	for _, r := range propre {
		if r != '\n' && r != '\t' && (r < 0x20 || (r >= 0x7f && r <= 0x9f)) {
			t.Fatalf("caractère de contrôle %U passé au terminal : %q", r, out.String())
		}
	}
	if !strings.HasPrefix(out.String(), vert+"ok: [kodi]"+fin) {
		t.Errorf("la couleur d'Ansible a été retirée : %q", out.String())
	}
}

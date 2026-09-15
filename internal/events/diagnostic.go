package events

import "encoding/json"

// CleErreur nomme le champ d'un payload écrit par WARDEN plutôt que recopié
// d'`ansible-runner`.
//
// Il vit dans le socle partagé — et non chez le runner qui l'écrit — parce que
// les DEUX extrémités en ont besoin : le runner pour le poser, la CLI pour
// l'afficher. L'ADR-0011 interdit à la CLI d'importer le runner, et recopier la
// chaîne des deux côtés ferait d'une faute de frappe un message qui disparaît
// sans que rien n'échoue.
//
// L'ajout au socle est délibérément minuscule : l'ADR-0011 note que tout ce qui
// y entre grossit aussi l'image hostile.
const CleErreur = "warden_error"

// Diagnostic rend le message que warden a écrit dans un payload, vide sinon.
//
// C'est le DISCRIMINANT qui permet d'afficher ce message-ci sans rouvrir la
// porte au bruit de cycle de vie d'Ansible : un run qui réussit émet déjà des
// events `verbose` sans hôte — MESURÉ : cinq, rien que pour l'avertissement de
// découverte d'interpréteur Python — que l'interface et la CLI écartent à juste
// titre. Sans clé propre, les montrer reviendrait à montrer aussi ceux-là.
//
// Tolérante par construction : un payload illisible ou d'une autre forme rend
// une chaîne vide, jamais une erreur. Elle est appelée sur CHAQUE event, y
// compris ceux d'un pod hostile, et un diagnostic ne doit jamais pouvoir faire
// échouer un affichage.
func Diagnostic(payload json.RawMessage) string {
	if len(payload) == 0 {
		return ""
	}
	var p map[string]json.RawMessage
	if err := json.Unmarshal(payload, &p); err != nil {
		return ""
	}
	brut, ok := p[CleErreur]
	if !ok {
		return ""
	}
	var msg string
	if err := json.Unmarshal(brut, &msg); err != nil {
		return ""
	}
	return msg
}

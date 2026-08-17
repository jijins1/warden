package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/jijins1/warden/internal/events"
	"github.com/jijins1/warden/internal/runner/internal/enroll"
	"github.com/jijins1/warden/internal/runner/internal/report"
	wire "github.com/jijins1/warden/internal/wire/dispatch"
	ingestwire "github.com/jijins1/warden/internal/wire/ingest"
)

// enroler pose la clé de l'environnement sur une machine et rapporte.
//
// AUCUN fetch, aucun playbook, aucun `ansible-runner` : un enrôlement ne
// matérialise pas de commit et n'exécute pas de code de l'utilisateur. C'est
// d'ailleurs la seule mission d'un pod warden qui n'exécute RIEN d'arbitraire —
// le §2 ne s'y applique donc pas de la même façon, ce qui ne change rien à ce
// que le pod a le droit de faire.
//
// Le récit passe par le MÊME chemin que les events d'un run : l'enrôlement est
// une action sur une machine de production par une personne nommée, donc de
// l'audit au même titre qu'un playbook.
func enroler(ctx context.Context, ordre wire.Enroll, token string) error {
	rapporteur, err := report.New(os.Getenv(ingestwire.EnvIngestURL), token, nil)
	if err != nil {
		return fmt.Errorf("enrôlement de %s : remontée des events : %w", ordre.Host, err)
	}

	slog.Info("enrôlement", slog.String("hôte", ordre.Host),
		slog.String("adresse", ordre.Address), slog.String("compte", ordre.Login),
		slog.Bool("épinglée", ordre.HostKey != ""))

	// Le numéro de tâche croît à chaque étape : c'est ce qui rend le récit
	// lisible dans l'ordre côté audit, où les events ne sont pas garantis
	// ordonnés à l'arrivée.
	var etape int
	dire := func(texte string) {
		etape++
		rapporteur.Send(evenementEnrolement(ordre.Host, etape, texte, events.StateOK))
	}

	res, errEnrol := enroll.Enroler(ctx, enroll.Cible{
		Host:    ordre.Host,
		Address: ordre.Address,
		Port:    ordre.Port,
		Login:   ordre.Login,
		// `Reveal` : le seul endroit où le mot de passe sort de son enveloppe
		// masquée, et il doit se voir en revue.
		Password:  ordre.Password.Reveal(),
		PublicKey: ordre.PublicKey,
		HostKey:   ordre.HostKey,
	}, dire)

	// L'EMPREINTE est rapportée quoi qu'il arrive, y compris en échec : c'est
	// elle que la CLI affiche et que l'opérateur épingle, et un enrôlement qui
	// échoue après l'avoir constatée a quand même appris quelque chose.
	if res.HostKey != "" {
		etape++
		rapporteur.Send(evenementResultat(ordre.Host, etape, res))
	}

	if errEnrol != nil {
		etape++
		etat := events.StateFailed
		if errors.Is(errEnrol, enroll.ErrEmpreinteChangee) {
			// `unreachable` et non `failed` : la machine jointe n'est pas celle
			// qu'on visait. La distinction se lit dans la colonne d'état, donc
			// dans une requête d'audit, pas seulement dans un message.
			etat = events.StateUnreachable
		}
		rapporteur.Send(evenementEnrolement(ordre.Host, etape, errEnrol.Error(), etat))

		slog.Error("enrôlement en échec", slog.String("hôte", ordre.Host),
			slog.String("error", errEnrol.Error()))
		return joindre(clore(ctx, rapporteur, ingestwire.StatusFailed, ""),
			fmt.Errorf("enrôlement de %s : %w", ordre.Host, errEnrol))
	}

	if errReport := rapporteur.Err(); errReport != nil {
		// Un enrôlement dont l'audit ne s'écrit pas ne peut pas être déclaré
		// réussi : personne ne saurait quelle machine a reçu la clé (§6.3).
		return joindre(clore(ctx, rapporteur, ingestwire.StatusFailed, ""),
			fmt.Errorf("enrôlement de %s : %w", ordre.Host, errReport))
	}

	slog.Info("enrôlement réussi", slog.String("hôte", ordre.Host),
		slog.String("empreinte", res.Fingerprint))
	return clore(ctx, rapporteur, ingestwire.StatusSucceeded, "")
}

// evenementEnrolement emballe une étape du récit.
//
// `Host` porte la MACHINE ENRÔLÉE et non le pod : c'est par lui que la vue par
// hôte du §6.5 rapprochera cet enrôlement des runs qui ont suivi sur la même
// machine.
func evenementEnrolement(hote string, etape int, texte string, etat events.State) events.Event {
	charge, err := json.Marshal(map[string]string{"warden_enrolement": texte})
	if err != nil {
		charge = json.RawMessage(`{"warden_enrolement":"<non encodable>"}`)
	}
	return events.Event{
		OccurredAt: time.Now().UTC(),
		Host:       hote,
		TaskNumber: etape,
		TaskName:   "enrôlement",
		State:      etat,
		Payload:    charge,
	}
}

// evenementResultat porte l'empreinte constatée, sous une forme exploitable.
//
// Un payload structuré et non une phrase : c'est la CLI qui le relit pour
// proposer l'épinglage, et lui faire découper un message en français serait le
// meilleur moyen de casser au premier changement de formulation.
func evenementResultat(hote string, etape int, res enroll.Resultat) events.Event {
	charge, err := json.Marshal(map[string]any{
		"warden_host_key":      res.HostKey,
		"warden_fingerprint":   res.Fingerprint,
		"warden_deja_presente": res.DejaPresente,
	})
	if err != nil {
		charge = json.RawMessage(`{"warden_host_key":""}`)
	}
	return events.Event{
		OccurredAt: time.Now().UTC(),
		Host:       hote,
		TaskNumber: etape,
		TaskName:   "empreinte",
		// `changed` quand la clé a été ajoutée : c'est le compteur d'audit
		// principal du §6.5, et un enrôlement QUI MODIFIE une machine doit y
		// entrer. Un réenrôlement sans effet, non.
		State:   etatDePose(res),
		Payload: charge,
	}
}

func etatDePose(res enroll.Resultat) events.State {
	if res.DejaPresente {
		return events.StateOK
	}
	return events.StateChanged
}

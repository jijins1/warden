package ingest

import (
	"errors"
	"fmt"
	"time"

	"github.com/jijins1/warden/internal/events"
)

// Status est l'issue d'un run telle que le runner la rapporte.
//
// Les valeurs répètent celles du §6.4 sans les emprunter au cœur du control
// plane : le pod ne peut pas importer `internal/server/internal/core`
// (ADR-0011), et c'est bien ainsi. Le contrat de fil est la frontière ; le
// serveur traduit ces chaînes en `core.RunState` chez lui, et une valeur qu'il
// ne connaît pas est refusée là-bas, pas ici.
type Status string

const (
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	// StatusInterrupted couvre l'annulation du §5.6. Elle EXIGE une cause :
	// « interrompu » sans la cause est presque inutile.
	StatusInterrupted Status = "interrupted"
)

// Cause qualifie un run interrompu (§6.4).
//
// Seules figurent ici les causes qu'un runner peut CONSTATER. Les causes
// d'infrastructure — OOM, éviction, nœud perdu — n'y sont pas, et leur absence
// est le fait central : dans ces cas-là le processus qui aurait dû les
// rapporter est mort. Elles ne peuvent venir que du watch informer du §6.4,
// donc du rôle `controller`.
type Cause string

const (
	// CauseUserInterrupt : le control plane a demandé l'arrêt (§5.6).
	CauseUserInterrupt Cause = "user_interrupt"
	// CauseUnknown est l'aveu honnête. Le §6.4 la garde délibérément : la
	// supprimer par principe conduirait à inventer une cause plausible, bien
	// pire en post-mortem.
	CauseUnknown Cause = "unknown"
)

// Final clôt un run.
//
// Il voyage DANS le lot d'events plutôt que sur une requête à lui, et c'est un
// choix de correction. Le §6.3 exige que les events soient persistés au fil de
// l'eau ; deux requêtes distinctes laisseraient une fenêtre où le run est déjà
// clos alors que ses derniers events ne sont pas journalisés — précisément
// l'état où l'on ne peut plus répondre à « qu'est-ce qui avait déjà été
// modifié ».
type Final struct {
	Status Status `json:"status"`

	// Cause est exigée si et seulement si Status vaut StatusInterrupted.
	Cause Cause `json:"cause,omitempty"`

	// SecretsUnmasked porte le MOTIF pour lequel la rédaction de l'ADR-0017
	// n'a pas pu s'appliquer, vide quand elle s'est appliquée normalement.
	//
	// Le motif et non un booléen : savoir qu'un masquage a échoué ne dit pas
	// s'il faut faire tourner un secret, et c'est la seule question qui compte
	// à ce moment-là.
	SecretsUnmasked string `json:"secrets_unmasked,omitempty"`
}

// Batch est le message montant runner → ingestion (ADR-0008).
//
// Le `run_id` n'y figure PAS, et son absence est une propriété de sécurité, pas
// une économie : le serveur résout `pod -> run` depuis le jeton présenté
// (ADR-0023). Un identifiant dans le corps inviterait à s'en servir, et
// l'ADR-0021 fait de ce point une condition de validité du schéma — l'`id` de
// run est un entier séquentiel, donc devinable.
type Batch struct {
	Events []events.Event `json:"events,omitempty"`

	// Final, non nil, clôt le run. Il accompagne le DERNIER lot.
	Final *Final `json:"final,omitempty"`
}

// Ack dit jusqu'où l'ingestion a durablement journalisé.
//
// L'accusé porte un offset et non un simple « reçu » : c'est ce qui permet au
// runner de relâcher son tampon en sachant ce qui est acquis. Une réémission
// reste sans danger — l'insertion est idempotente sur (run_id, seq) — mais
// l'accusé évite d'en faire le cas normal.
type Ack struct {
	AckedSeq int64 `json:"acked_seq"`
}

// Plafonds du lot, partagés par les deux extrémités.
//
// Ils vivent ICI et non de chaque côté pour la raison écrite dans
// `wire/dispatch` : deux extrémités qui déclarent la même convention chacune de
// son côté finissent par diverger, et la divergence ne se voit qu'à
// l'exécution.
const (
	// MaxEventsPerBatch borne le nombre d'events d'un lot.
	//
	// En dessous du plafond d'insertion de `store` (500 events par instruction)
	// afin qu'un lot reste UNE instruction, donc un seul déclenchement du
	// trigger qui tient le compteur `changed`.
	MaxEventsPerBatch = 200

	// MaxBatchBytes borne le corps d'une requête d'ingestion.
	//
	// Le payload d'un event n'est pas borné par le schéma — un `stdout` de
	// module peut être énorme — et cette borne est ce qui empêche un pod
	// hostile de faire tenir un lot arbitraire en mémoire du rôle d'ingestion.
	// Un lot qui la dépasse est refusé, pas tronqué : tronquer produirait du
	// JSON invalide et une erreur qui parlerait d'autre chose.
	MaxBatchBytes = 8 << 20 // 8 Mio

	// FlushInterval est le délai au-delà duquel un lot part même incomplet.
	//
	// C'est la latence que voit l'utilisateur devant son stream, et le grain de
	// l'exigence « au fil de l'eau » du §6.3. L'ADR-0008 la situe « de l'ordre
	// de quelques centaines de millisecondes ».
	FlushInterval = 250 * time.Millisecond
)

// Path est le chemin de l'endpoint, relatif à EnvIngestURL.
//
// Ici et pas de chaque côté, pour la raison qui vaut déjà pour les noms de
// variables : le runner le compose, l'ingestion le sert, et deux littéraux
// jumeaux finissent par diverger — la panne se présentant alors comme un 404
// que rien dans les deux moitiés, chacune testée, n'aurait pu montrer.
const Path = "/events"

// EnvIngestURL est l'adresse de l'ingestion telle qu'un POD la joint.
//
// Déclarée dans le contrat de fil, au même titre que `EnvDispatchURL` : c'est
// l'orchestrateur qui la pose et le runner qui la lit, et la faire déclarer par
// les deux a déjà produit une panne — `WARDEN_DISPATCH_URL` n'était posée par
// personne alors que le runner l'exigeait, les deux moitiés étant par ailleurs
// cohérentes et testées.
const EnvIngestURL = "WARDEN_INGEST_URL"

// ErrProtocol est la classe d'erreur des lots refusés à la validation.
var ErrProtocol = errors.New("protocole d'ingestion")

// Valid dit si le statut fait partie de ceux que le contrat accepte.
func (s Status) Valid() bool {
	switch s {
	case StatusSucceeded, StatusFailed, StatusInterrupted:
		return true
	}
	return false
}

// Valid dit si la cause fait partie de celles qu'un runner peut constater.
func (c Cause) Valid() bool {
	switch c {
	case CauseUserInterrupt, CauseUnknown:
		return true
	}
	return false
}

// Validate applique les invariants du contrat.
//
// Elle est appelée des DEUX côtés : par le runner avant d'émettre, pour ne pas
// dépenser une reprise sur un lot qui sera refusé de toute façon, et par
// l'ingestion à la réception, parce que le runner est du code hostile et que ce
// qu'il promet ne vaut rien.
//
// Elle nomme le seq fautif. Un lot refusé sans dire QUEL event est en cause
// laisse l'émetteur sans recours : il ne peut que tout réémettre, ce qui
// échouera de la même façon.
func (b Batch) Validate() error {
	if len(b.Events) == 0 && b.Final == nil {
		// Un lot vide n'est pas une erreur de transport, c'est une requête qui
		// ne demande rien. La refuser ici évite qu'un défaut de cadencement
		// devienne un martèlement silencieux de l'ingestion.
		return fmt.Errorf("%w: lot sans event ni trame terminale", ErrProtocol)
	}
	if len(b.Events) > MaxEventsPerBatch {
		return fmt.Errorf("%w: %d events, plafond %d",
			ErrProtocol, len(b.Events), MaxEventsPerBatch)
	}

	// Les seq doivent être STRICTEMENT croissants dans un lot. L'insertion est
	// idempotente sur (run_id, seq) : deux events du même seq dans un même lot
	// n'en feraient qu'un, et le second serait perdu sans que rien ne le dise.
	precedent := int64(-1)
	for _, e := range b.Events {
		if err := e.Validate(); err != nil {
			return fmt.Errorf("%w: %w", ErrProtocol, err)
		}
		if e.Seq <= precedent {
			return fmt.Errorf("%w: seq %d après seq %d, l'ordre doit être strictement croissant",
				ErrProtocol, e.Seq, precedent)
		}
		precedent = e.Seq
	}

	if b.Final != nil {
		if err := b.Final.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// Validate applique les invariants de la trame terminale.
func (f Final) Validate() error {
	if !f.Status.Valid() {
		return fmt.Errorf("%w: statut de run %q inconnu", ErrProtocol, f.Status)
	}
	if f.Status == StatusInterrupted {
		if !f.Cause.Valid() {
			return fmt.Errorf("%w: cause de terminaison %q inconnue", ErrProtocol, f.Cause)
		}
	} else if f.Cause != "" {
		// Le schéma impose déjà l'équivalence `state = interrupted` ⇔ cause non
		// nulle. La vérifier ici fait dire au message ce qui ne va pas, au lieu
		// de renvoyer une violation de contrainte PostgreSQL.
		return fmt.Errorf("%w: cause %q sur un run %s", ErrProtocol, f.Cause, f.Status)
	}
	return nil
}

package api

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/jijins1/warden/internal/server/internal/core"
)

// enrollPayload est ce que `warden enroll` envoie.
//
// C'est le seul corps de tout le produit qui porte le mot de passe d'un HUMAIN.
// Il n'est ni journalisé, ni écrit en base, ni rendu : il est rangé dans une
// consigne éphémère et détruit à la livraison de l'ordre (ADR-0018).
type enrollPayload struct {
	// RunRef est composé par la CLI, comme pour un run de playbook : rejouer
	// une soumission interrompue avec le même identifiant est ce qui rend la
	// reprise sûre.
	RunRef      string `json:"run_ref"`
	Environment string `json:"environment"`

	Host    string `json:"host"`
	Address string `json:"address"`
	Port    int    `json:"port,omitempty"`
	Login   string `json:"login"`

	// Password ne survit pas à cette requête : il part en consigne, et la
	// livraison de l'ordre le consomme.
	Password string `json:"password"`
}

// enrollCreated est ce que le serveur rend.
//
// Le `run_ref` est l'essentiel : c'est par lui que la CLI suit le déroulement,
// récupère l'empreinte constatée et déclare ensuite la machine.
type enrollCreated struct {
	RunRef string `json:"run_ref"`
	Pod    string `json:"pod,omitempty"`
}

// tailleMaxEnrolement borne le corps. Une cible et un mot de passe font quelques
// centaines d'octets.
const tailleMaxEnrolement = 8 << 10

// HandleCreateEnrollment déclare et démarre un run d'enrôlement.
//
// POST /admin/enrollments
//
// L'ORDRE porte la correction, et il est celui de `POST /runs` :
//
//  1. le mot de passe part en CONSIGNE, avant tout le reste. S'il n'y arrive
//     pas, rien n'a été créé ;
//  2. le run est ENREGISTRÉ, dans l'état `interrupted` / `unknown` (§6.4) : un
//     plantage ici laisse une ligne qui dit la vérité ;
//  3. le pod est lancé en dernier, quand tout ce dont il a besoin existe.
func (a *Server) HandleCreateEnrollment(w http.ResponseWriter, r *http.Request) {
	if !a.adminOuRien(w, r) {
		return
	}
	if a.deps.Creator == nil || a.deps.Starter == nil || a.deps.Stash == nil {
		// Un enrôlement suppose le cluster : sans lui il n'y a ni pod pour se
		// connecter, ni consigne où ranger le mot de passe. Le dire vaut mieux
		// que d'enregistrer un run qui ne démarrera jamais.
		http.Error(w, "assisted enrollment is not available on this deployment",
			http.StatusNotImplemented)
		return
	}

	var p enrollPayload
	if !decoderJSON(w, r, tailleMaxEnrolement, &p) {
		return
	}
	if p.Password == "" {
		http.Error(w, "password is required", http.StatusBadRequest)
		return
	}
	if p.RunRef == "" {
		http.Error(w, "run_ref is required", http.StatusBadRequest)
		return
	}

	port := p.Port
	if port == 0 {
		port = core.PortSSHParDefaut
	}
	cible := core.EnrollTarget{Host: p.Host, Address: p.Address, Port: port, Login: p.Login}
	if err := cible.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := core.ValidateEnvironment(p.Environment); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// L'environnement DOIT avoir une clé : c'est elle qu'on va poser. Le
	// vérifier ici plutôt qu'à la livraison évite de lancer un pod, de lui faire
	// ouvrir une session avec le mot de passe de quelqu'un, et de constater
	// alors qu'il n'y a rien à installer.
	if a.deps.Keys != nil {
		if _, err := a.deps.Keys.KeyByEnvironment(r.Context(), p.Environment); err != nil {
			if errors.Is(err, core.ErrNoKey) {
				http.Error(w, "environment "+p.Environment+" has no access key: "+
					"run `warden key generate --env "+p.Environment+"` first",
					http.StatusBadRequest)
				return
			}
			slog.Error("lecture de la clé en échec", slog.String("error", err.Error()))
			http.Error(w, "could not read the environment key", http.StatusInternalServerError)
			return
		}
	}

	user := ContextUser(r.Context())

	// 1. La consigne d'abord. Elle est écrasée si elle existe : reprendre un
	// enrôlement interrompu avec le même `run_ref` doit remplacer le mot de
	// passe précédent, pas échouer sur sa présence.
	if err := a.deps.Stash.Stash(r.Context(), p.RunRef, []byte(p.Password)); err != nil {
		slog.Error("consigne du mot de passe en échec",
			slog.String("run_ref", p.RunRef), slog.String("error", err.Error()))
		http.Error(w, "could not prepare the enrollment", http.StatusInternalServerError)
		return
	}

	// 2. Le run. `repo` est vide et le restera : un enrôlement ne tire aucun
	// code.
	id, err := a.deps.Creator.CreateRun(r.Context(), core.NewRun{
		RunRef: p.RunRef, UserID: user.ID,
		Environment:  p.Environment,
		Kind:         core.RunEnrollment,
		EnrollTarget: &cible,
	})
	if errors.Is(err, core.ErrRunRefExists) {
		a.enrolementDejaConnu(w, r, p)
		return
	}
	if err != nil {
		slog.Error("création du run d'enrôlement en échec",
			slog.String("run_ref", p.RunRef), slog.String("error", err.Error()))
		http.Error(w, "could not record the enrollment", http.StatusInternalServerError)
		return
	}

	slog.Info("enrôlement déclaré",
		slog.String("run_ref", p.RunRef), slog.String("hôte", p.Host),
		slog.String("environnement", p.Environment), slog.Int64("par", user.ID))

	// 3. Le pod.
	pod, err := a.deps.Starter.Start(r.Context(), core.Run{
		ID: id, RunRef: p.RunRef, UserID: user.ID,
		Environment: p.Environment, Kind: core.RunEnrollment,
		EnrollTarget: &cible,
	})
	if err != nil {
		slog.Error("démarrage de l'enrôlement en échec",
			slog.String("run_ref", p.RunRef), slog.String("error", err.Error()))
		http.Error(w, "enrollment "+p.RunRef+" is recorded but could not be started",
			http.StatusBadGateway)
		return
	}

	if a.deps.Running != nil {
		if err := a.deps.Running.MarkRunning(r.Context(), id, pod); err != nil {
			slog.Error("enrôlement non marqué courant",
				slog.String("run_ref", p.RunRef), slog.String("error", err.Error()))
		}
	}

	w.WriteHeader(http.StatusCreated)
	ecrireJSON(w, enrollCreated{RunRef: p.RunRef, Pod: pod})
}

// enrolementDejaConnu répond quand le `run_ref` soumis existe déjà.
//
// La CLI réutilise le même identifiant pour REPRENDRE un enrôlement interrompu,
// et l'étape 1 de la création est idempotente exprès pour cela. L'étape 2 ne
// peut pas l'être : un run est un enregistrement d'AUDIT, et le recréer sous le
// même identifiant effacerait le premier — ce que le produit existe précisément
// pour empêcher.
//
// Reste donc à dire ce qu'est devenu le run, et quoi faire. C'est tout l'écart
// entre un 500 « could not record the enrollment », qui parle de PostgreSQL, et
// une réponse dont l'utilisateur peut se servir.
func (a *Server) enrolementDejaConnu(w http.ResponseWriter, r *http.Request, p enrollPayload) {
	ctx := r.Context()

	// La consigne posée à l'étape 1 n'a plus de destinataire : aucun pod ne
	// viendra la lire. La CONSOMMER la détruit — la laisser vivre laisserait
	// dormir le mot de passe de quelqu'un dans un Secret que rien ne relira.
	if _, err := a.deps.Stash.Take(ctx, p.RunRef); err != nil && !errors.Is(err, core.ErrNoStash) {
		slog.Warn("consigne d'un enrôlement refusé non reprise",
			slog.String("run_ref", p.RunRef), slog.String("error", err.Error()))
	}

	refuser := func(message string) {
		http.Error(w, message, http.StatusConflict)
	}

	// Le message générique, utilisé chaque fois qu'on ne peut pas DÉCRIRE le run
	// sans risque : illisible, ou appartenant à quelqu'un d'autre. Décrire le run
	// d'autrui ferait de cet endpoint un oracle, ce que `requireRunAccess` refuse
	// déjà ailleurs pour la même raison.
	const generique = "run_ref is already in use: start a fresh enrollment without --run-ref"

	existant, err := a.deps.Runs.RunByRef(ctx, p.RunRef)
	if err != nil {
		refuser(generique)
		return
	}
	if user := ContextUser(ctx); user == nil || existant.UserID != user.ID {
		refuser(generique)
		return
	}
	if existant.Kind != core.RunEnrollment {
		refuser(generique)
		return
	}

	if existant.State == core.RunRunning {
		refuser("enrollment " + p.RunRef + " is already running: follow it instead of starting it again")
		return
	}
	if existant.State != core.RunSucceeded {
		refuser("enrollment " + p.RunRef + " already ended (" + string(existant.State) +
			"): start a fresh enrollment without --run-ref")
		return
	}

	// Le cas qui a motivé tout ceci : l'enrôlement a RÉUSSI, donc la clé est
	// posée sur la machine, et seule la déclaration a été refusée. Renvoyer
	// « déjà utilisé » ferait recommencer un travail déjà fait ; ce qu'il faut
	// dire est qu'il ne reste que l'inventaire.
	hote := p.Host
	if existant.EnrollTarget != nil {
		hote = existant.EnrollTarget.Host
	}
	if a.deps.Hosts != nil {
		if _, err := a.deps.Hosts.HostByName(ctx, existant.Environment, hote); err == nil {
			refuser("enrollment " + p.RunRef + " already completed and " + hote +
				" is declared: start a fresh enrollment without --run-ref")
			return
		}
	}
	refuser("enrollment " + p.RunRef + " already completed: the key is installed on " + hote +
		", only the inventory entry is missing. Declare it with `warden host declare`, " +
		"or start a fresh enrollment without --run-ref")
}

// Package server est le point d'entrée du control plane.
//
// Il expose les seuls symboles que `cmd/wardend` peut atteindre : tout le reste
// vit sous `internal/server/internal/`, donc hors de portée des autres domaines
// de confiance — refusé par le compilateur, pas par une revue (ADR-0011).
//
// Ce paquet doit rester réduit au type Role et à Run : il est le seul du domaine
// serveur que `internal/cli` et `internal/runner` peuvent importer.
package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/jijins1/warden/internal/git"
	"github.com/jijins1/warden/internal/server/internal/core"
	"github.com/jijins1/warden/internal/server/internal/gitd"
	"github.com/jijins1/warden/internal/server/internal/inbound/api"
	"github.com/jijins1/warden/internal/server/internal/inbound/ingest"
	"github.com/jijins1/warden/internal/server/internal/outbound/activedirectory"
	"github.com/jijins1/warden/internal/server/internal/outbound/dispatch"
	"github.com/jijins1/warden/internal/server/internal/outbound/flux"
	"github.com/jijins1/warden/internal/server/internal/outbound/gitclient"
	"github.com/jijins1/warden/internal/server/internal/outbound/grantfile"
	"github.com/jijins1/warden/internal/server/internal/outbound/keyinventory"
	"github.com/jijins1/warden/internal/server/internal/outbound/orchestrator"
	"github.com/jijins1/warden/internal/server/internal/outbound/protectedfile"
	"github.com/jijins1/warden/internal/server/internal/outbound/store"
	"github.com/jijins1/warden/internal/server/internal/outbound/vaultinventory"
)

// Role désigne le rôle sous lequel le binaire est lancé (ADR-0006).
type Role string

const (
	// RoleAPI sert l'API HTTP + WS, et l'ingestion des events sur un port
	// distinct (ADR-0008).
	RoleAPI Role = "api"
	// RoleIngest ne sert que l'ingestion. Non déployé aujourd'hui : il existe
	// pour que sa promotion en rôle déployé à part reste un changement de
	// déploiement, jamais un changement de code (ADR-0008).
	RoleIngest Role = "ingest"
	// RoleGit détient le volume des dépôts bare. Une seule instance (ADR-0006).
	RoleGit Role = "git"
	// RoleController réconcilie les pods runner. Un seul actif, leader election
	// par Lease (ADR-0006).
	RoleController Role = "controller"
)

// Roles énumère les rôles acceptés, dans l'ordre d'affichage de l'usage.
func Roles() []Role {
	return []Role{RoleAPI, RoleIngest, RoleGit, RoleController}
}

// ErrUnknownRole est retournée pour un rôle non reconnu.
var ErrUnknownRole = errors.New("rôle inconnu")

// ParseRole valide une chaîne fournie en line de commande.
func ParseRole(s string) (Role, error) {
	for _, r := range Roles() {
		if Role(s) == r {
			return r, nil
		}
	}
	return "", fmt.Errorf("%w: %q", ErrUnknownRole, s)
}

// Run exécute le rôle demandé jusqu'à l'annulation du contexte.
func Run(ctx context.Context, r Role) error {
	if _, err := ParseRole(string(r)); err != nil {
		return err
	}

	switch r {
	case RoleAPI:
		return runAPI(ctx)
	case RoleIngest:
		return runIngest(ctx)
	case RoleGit:
		return runGit(ctx)
	case RoleController:
		return runController(ctx)
	default:
		return fmt.Errorf("rôle %q: pas encore implémenté", r)
	}
}

// runIngest ne sert que l'ingestion.
//
// Le rôle existe pour que sa promotion en processus déployé à part reste un
// changement de DÉPLOIEMENT et jamais un changement de code (ADR-0008). Il
// n'est pas déployé aujourd'hui — `api` sert le même endpoint sur le même port
// distinct — et cette fonction est ce qui rend la promesse vérifiable.
//
// Deux connexions et pas une : la résolution `pod -> run` passe par le magasin
// applicatif, l'écriture par le pool restreint. Les confondre donnerait au
// chemin d'écriture les droits du chemin de lecture, ce que toute la séparation
// de rôles existe pour empêcher.
func runIngest(ctx context.Context) error {
	adresse := os.Getenv(EnvIngestAddr)
	if adresse == "" {
		// Un rôle d'ingestion sans adresse d'écoute démarrerait, ne servirait
		// rien, et paraîtrait sain. Le refus est ici.
		return fmt.Errorf("rôle ingest : %s est requise", EnvIngestAddr)
	}

	dsn, err := databaseURL(EnvDatabaseURL)
	if err != nil {
		return err
	}

	st, err := store.Open(ctx, dsn)
	if err != nil {
		return fmt.Errorf("ouverture du magasin: %w", err)
	}
	defer st.Close()

	h, fermer, err := ouvrirIngestion(ctx, dsn, st)
	if err != nil {
		return err
	}
	defer fermer()

	return servirIngestion(ctx, adresse, h)
}

// runController réconcilie l'état de la base sur ce que le cluster montre.
//
// C'est la marche 3 de la machine à états du §6.4, et sans elle le produit MENT :
// un run dont le pod est évincé, tué par l'OOM ou emporté avec son nœud reste
// « en cours » pour toujours, alors que la question d'audit qui compte — qu'est-ce
// qui avait déjà été modifié — a une réponse dans le journal d'events.
//
// Il ne sert AUCUN port. C'est délibéré : rien ne doit pouvoir le solliciter de
// l'extérieur, et il n'a rien à répondre à personne.
//
// PAS DE LEADER ELECTION à ce stade, alors que l'ADR-0006 la prévoit. Elle ne
// changerait rien à la correction : la garde `ended_at IS NULL` rend la clôture
// idempotente, et le ramassage ne touche que des pods déjà terminés. Deux
// contrôleurs en parallèle feraient donc du travail en double, pas du travail
// faux — et le Lease s'ajoutera quand le coût du doublon comptera.
func runController(ctx context.Context) error {
	dsn, err := databaseURL(EnvDatabaseURL)
	if err != nil {
		return err
	}
	m, err := lireModes()
	if err != nil {
		return err
	}
	ns := os.Getenv(EnvPodNamespace)
	if m.orchestrateur == modeKubernetes && ns == "" {
		// Sans namespace, il n'y a rien à observer. Démarrer quand même donnerait
		// un contrôleur qui tourne, ne réconcilie rien et paraît sain — la pire
		// des trois issues.
		return fmt.Errorf("rôle controller : %s est requise", EnvPodNamespace)
	}

	st, err := store.Open(ctx, dsn)
	if err != nil {
		return fmt.Errorf("ouverture du magasin: %w", err)
	}
	defer st.Close()

	// Les adresses de dispatch et d'ingestion sont exigées par le constructeur de
	// l'orchestrateur parce qu'un pod LANCÉ sans elles ne saurait qui appeler. Le
	// contrôleur n'en lance aucun ; les valeurs posées ici ne partent nulle part,
	// et les inventer serait plus honnête que d'affaiblir un contrôle qui protège
	// le chemin de lancement.
	dispatchURL := valeurOuDefaut(EnvDispatchRunnerURL, "http://unused.controller.invalid")
	ingestURL := valeurOuDefaut(EnvIngestRunnerURL, "http://unused.controller.invalid")

	var pods core.PodsObserves
	switch m.orchestrateur {
	case modeDocker:
		// En mode Docker, c'est le contrôleur qui porte la borne de durée
		// (ADR-0047) : il lui faut donc le même plafond que le lanceur.
		deadline, err := secondesDeDeadline()
		if err != nil {
			return err
		}
		d, err := lanceurDocker(dispatchURL, ingestURL, deadline)
		if err != nil {
			return err
		}
		pods = d
	default:
		k8s, err := clientKubernetes()
		if err != nil {
			return err
		}
		orch, err := orchestrator.New(k8s, orchestrator.Config{
			Namespace:   ns,
			DispatchURL: dispatchURL,
			IngestURL:   ingestURL,
			PullSecrets: secretsDeTirage(),
		})
		if err != nil {
			return err
		}
		pods = orch
	}

	rec, err := core.NewReconciler(st, pods)
	if err != nil {
		return err
	}

	// Les DROITS sont réconciliés par le contrôleur, et pas par le rôle `api`.
	//
	// C'est le rôle dont le métier est justement de faire correspondre la base à
	// ce que le monde extérieur dit — ici un fichier plutôt qu'un cluster. Et il
	// tourne à UN réplica : le mettre dans `api`, qui peut en avoir N, ferait
	// tourner N réconciliateurs sur le même document.
	droits := grantfile.New(os.Getenv(EnvGrantsFile))

	// Les environnements PROTÉGÉS (ADR-0044) suivent le même principe : c'est
	// le rôle dont le métier est de faire correspondre la base à un fichier,
	// et il tourne à UN réplica.
	proteges := protectedfile.New(os.Getenv(EnvProtectedFile))

	// DEUX boucles, DEUX rythmes, et c'est une correction d'un couplage initial.
	//
	// Les avoir liés paraissait économe — un seul minuteur, un seul endroit où se
	// tromper — mais ça mettait deux besoins opposés sur la même horloge :
	//
	//   - les PODS coûtent un LIST à l'API Kubernetes à chaque passage, et le
	//     retard qu'ils bornent est un retard d'AUDIT : un run tué met une minute
	//     à être clos, ce qui ne gêne personne. On veut pouvoir ralentir.
	//   - les DROITS coûtent une lecture de fichier et une comparaison de
	//     chaîne — quasiment rien tant que rien ne change — et le retard qu'ils
	//     bornent est un retard de SÉCURITÉ : révoquer un accès doit prendre effet
	//     vite. On veut pouvoir accélérer.
	//
	// Sur une horloge commune, accélérer la révocation aurait payé un LIST
	// Kubernetes toutes les dix secondes pour rien.
	intervallePods := secondesOuDefaut(EnvReconcileSeconds, core.IntervalleReconciliation)
	intervalleDroits := secondesOuDefaut(EnvGrantsSeconds, IntervalleDesDroits)
	intervalleProteges := secondesOuDefaut(EnvProtectedSeconds, IntervalleDesProteges)

	perimetre := slog.String("namespace", ns)
	if m.orchestrateur == modeDocker {
		perimetre = slog.String("instance", valeurOuDefaut(EnvDockerInstance, "warden"))
	}
	slog.Info("contrôleur démarré",
		slog.String("orchestrateur", m.orchestrateur),
		perimetre,
		slog.Duration("intervalle_pods", intervallePods))
	if droits != nil {
		slog.Info("droits lus depuis un fichier",
			slog.String("chemin", droits.Chemin()),
			slog.Duration("intervalle", intervalleDroits),
			slog.String("note", "c'est la seule source ; aucune commande ne les modifie"))
	}
	if proteges != nil {
		slog.Info("environnements protégés lus depuis un fichier",
			slog.String("chemin", proteges.Chemin()),
			slog.Duration("intervalle", intervalleProteges))
	}

	err = servirTous(ctx,
		func(ctx context.Context) error {
			return rec.Boucler(ctx, intervallePods, journaliserBilan)
		},
		func(ctx context.Context) error {
			return boucleDesDroits(ctx, droits, st, intervalleDroits)
		},
		func(ctx context.Context) error {
			return boucleDesProteges(ctx, proteges, st, intervalleProteges)
		},
	)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// IntervalleDesDroits est le rythme de relecture du document de droits.
//
// Dix secondes, contre trente pour les pods, et l'écart est délibéré. Un passage
// à vide ne coûte qu'une lecture de fichier et une comparaison de chaîne — pas
// de requête, ni à la base ni à Kubernetes — alors que ce qu'il borne est le
// délai de prise d'effet d'une RÉVOCATION.
//
// Le kubelet domine de toute façon : il met quelques dizaines de secondes à
// republier un ConfigMap modifié. Descendre sous dix secondes n'achèterait donc
// rien de mesurable.
const IntervalleDesDroits = 10 * time.Second

// boucleDesDroits relit le document et réconcilie, jusqu'à l'annulation.
//
// Sans source configurée, elle attend simplement l'annulation : rendre une
// erreur ferait tomber le contrôleur entier, alors qu'un déploiement sans
// document de droits est un déploiement valide — il n'accorde rien.
func boucleDesDroits(ctx context.Context, source *grantfile.Source, st *store.Store, intervalle time.Duration) error {
	if source == nil {
		<-ctx.Done()
		return ctx.Err()
	}
	if intervalle <= 0 {
		intervalle = IntervalleDesDroits
	}

	t := time.NewTicker(intervalle)
	defer t.Stop()

	for {
		// Un passage IMMÉDIAT au démarrage : le cas qui compte le plus est le
		// redémarrage du contrôleur après un changement de droits, où le document
		// attend déjà d'être appliqué.
		reconcilierLesDroits(ctx, source, st)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

// journaliserBilan rend un passage lisible sans le noyer.
//
// Le passage à vide est le cas NORMAL et il est journalisé en debug : à trente
// secondes d'intervalle, l'écrire en info remplirait les logs de bruit, et le
// bruit est ce qui fait qu'on ne voit pas la line qui compte.
func journaliserBilan(b core.Bilan, err error) {
	if err != nil {
		slog.Error("passage de réconciliation en échec",
			slog.String("error", err.Error()),
			slog.Int("examinés", b.Examines), slog.Int("clos", b.Clos))
		return
	}
	if b.Clos == 0 && b.Ramasses == 0 {
		slog.Debug("réconciliation : rien à faire", slog.Int("examinés", b.Examines))
		return
	}
	slog.Info("réconciliation",
		slog.Int("examinés", b.Examines),
		slog.Int("clos", b.Clos),
		slog.Int("ramassés", b.Ramasses))
}

// secondesDeDeadline lit le plafond de durée d'un run.
//
// Une valeur illisible fait ÉCHOUER le démarrage plutôt que retomber sur le
// défaut : « 30m » écrit là où des secondes sont attendues donnerait
// silencieusement deux heures, et personne ne le verrait avant d'avoir besoin du
// plafond — c'est-à-dire quand un run pend.
func secondesDeDeadline() (int64, error) {
	brut := os.Getenv(EnvRunDeadlineSeconds)
	if brut == "" {
		return 0, nil // l'orchestrateur applique son défaut
	}
	n, err := strconv.ParseInt(brut, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s=%q : un nombre de SECONDES strictement positif est attendu",
			EnvRunDeadlineSeconds, brut)
	}
	return n, nil
}

// secondesOuDefaut lit une durée exprimée en SECONDES.
//
// Une valeur illisible est ignorée avec un avertissement plutôt que fatale : ces
// deux réglages sont des rythmes, et leur défaut est sûr. Refuser de démarrer le
// contrôleur pour un intervalle mal écrit coûterait plus que ça ne protège — au
// contraire du plafond de durée d'un run, où un défaut silencieux de deux heures
// ne se verrait qu'au moment d'en avoir besoin.
func secondesOuDefaut(nom string, defaut time.Duration) time.Duration {
	brut := os.Getenv(nom)
	if brut == "" {
		return defaut
	}
	n, err := strconv.Atoi(brut)
	if err != nil || n <= 0 {
		slog.Warn("intervalle illisible, le défaut s'applique",
			slog.String("variable", nom), slog.String("valeur", brut),
			slog.Duration("défaut", defaut))
		return defaut
	}
	return time.Duration(n) * time.Second
}

// dureeDeSession lit la validité d'une session.
//
// Une valeur illisible est IGNORÉE plutôt que fatale, contrairement au plafond
// de durée d'un run : celui-ci protège d'un run qui pend, alors qu'ici le défaut
// est sûr. Refuser de démarrer le control plane entier pour une variable mal
// écrite coûterait plus que ça ne protège — mais l'avertissement doit se voir.
func dureeDeSession() time.Duration {
	brut := os.Getenv(EnvSessionTTL)
	if brut == "" {
		return 0 // le cœur applique son défaut
	}
	h, err := strconv.Atoi(brut)
	if err != nil || h <= 0 {
		slog.Warn("durée de session illisible, le défaut s'applique",
			slog.String("variable", EnvSessionTTL), slog.String("valeur", brut),
			slog.Duration("défaut", core.DureeSession))
		return 0
	}
	return time.Duration(h) * time.Hour
}

// brancherLAnnuaire fait passer la connexion par Active Directory quand
// WARDEN_LDAP_URL est posée (ADR-0053).
//
// Une configuration INCOMPLÈTE fait échouer le démarrage plutôt que retomber sur
// les comptes locaux : un déploiement qui croit avoir branché l'AD et ne l'a pas
// fait laisserait tout le monde dehors sans le dire.
func brancherLAnnuaire(auth *core.Authenticator, st *store.Store) error {
	adresse := os.Getenv(EnvLDAPURL)
	if adresse == "" {
		return nil
	}
	ad, err := activedirectory.New(activedirectory.Config{
		URL:          adresse,
		StartTLS:     os.Getenv(EnvLDAPStartTLS) == "1",
		BaseDN:       os.Getenv(EnvLDAPBaseDN),
		BindDN:       os.Getenv(EnvLDAPBindDN),
		BindPassword: os.Getenv(EnvLDAPBindPassword),
		CAFile:       os.Getenv(EnvLDAPCAFile),
	})
	if err != nil {
		return err
	}
	// Même règle que WARDEN_SESSION_TTL_HOURS : une valeur illisible laisse le
	// défaut du cœur, qui est sûr.
	var duree time.Duration
	if brut := os.Getenv(EnvLDAPSessionTTL); brut != "" {
		if h, err := strconv.Atoi(brut); err == nil && h > 0 {
			duree = time.Duration(h) * time.Hour
		} else {
			slog.Warn("durée de session AD illisible, le défaut s'applique",
				slog.String("variable", EnvLDAPSessionTTL), slog.String("valeur", brut),
				slog.Duration("défaut", core.DureeSessionAnnuaire))
		}
	}
	if err := auth.BrancherLAnnuaire(ad, st, duree); err != nil {
		return err
	}
	slog.Info("connexion par l'annuaire",
		slog.String("url", adresse),
		slog.String("note", "un compte local actif est vérifié d'abord ; sans lui, l'AD répond seul, sans repli"))
	return nil
}

// reconcilierLesDroits applique le document de droits s'il a changé.
//
// Adossée au passage du réconciliateur de pods plutôt qu'à sa propre boucle : le
// rythme est le même, et un second minuteur n'achèterait qu'un second endroit où
// se tromper d'intervalle.
//
// Aucune erreur n'est REMONTÉE : elles sont journalisées et le passage suivant
// réessaie. Faire échouer la boucle sur un YAML cassé arrêterait aussi la
// réconciliation des pods, qui n'a rien à voir — et laisserait des runs en vol.
func reconcilierLesDroits(ctx context.Context, source *grantfile.Source, st *store.Store) {
	if source == nil {
		return
	}

	doc, change, err := source.Lire()
	switch {
	case errors.Is(err, grantfile.ErrAbsent):
		// NE RIEN FAIRE. Un contrôleur démarré avant que le ConfigMap soit posé
		// ne doit pas conclure « personne n'a de droit » et tout révoquer.
		return
	case err != nil:
		slog.Error("document de droits illisible",
			slog.String("chemin", source.Chemin()), slog.String("error", err.Error()))
		return
	case !change:
		return
	}

	actuels, err := st.ListGrants(ctx, "")
	if err != nil {
		source.Oublier()
		slog.Error("lecture des droits en échec", slog.String("error", err.Error()))
		return
	}

	// Les GROUPES de l'annuaire (ADR-0053) : lus AVANT de planifier, pour que le
	// plan soit complet et s'applique en une transaction.
	groupes, err := st.ListGroupGrants(ctx)
	if err != nil {
		source.Oublier()
		slog.Error("lecture des droits de groupe en échec", slog.String("error", err.Error()))
		return
	}
	admins, err := st.GroupesAdmin(ctx)
	if err != nil {
		source.Oublier()
		slog.Error("lecture des groupes d'administration en échec", slog.String("error", err.Error()))
		return
	}
	plan, err := core.PlanifierLesDroits(doc, actuels, groupes, admins)
	if err != nil {
		// Le document est syntaxiquement lisible mais refusé : document vide,
		// environnement mal formé, compte vide. C'est une erreur de RÉDACTION, et
		// elle doit se voir dans les journaux du contrôleur — c'est le seul
		// endroit où elle apparaîtra, puisque personne ne lance de commande.
		slog.Error("document de droits refusé",
			slog.String("chemin", source.Chemin()), slog.String("error", err.Error()))
		return
	}

	if plan.Vide() {
		slog.Info("droits inchangés", slog.Int("conformes", plan.Inchanges))
		return
	}

	// `parQui` est ZÉRO : personne n'a accordé ces droits à la main. La trace dit
	// « le document », ce qui est vrai — l'auteur du changement est dans
	// l'historique git, pas ici.
	if err := st.AppliquerLePlan(ctx, plan, 0); err != nil {
		source.Oublier()
		slog.Error("application des droits en échec",
			slog.String("chemin", source.Chemin()), slog.String("error", err.Error()))
		return
	}

	for _, c := range plan.Revoquer {
		slog.Warn("droit révoqué par le document",
			slog.String("sujet", c.Subject), slog.String("environnement", c.Environment))
	}
	for _, c := range plan.Accorder {
		slog.Warn("droit accordé par le document",
			slog.String("sujet", c.Subject), slog.String("environnement", c.Environment))
	}
	for _, c := range plan.Groupes.Revoquer {
		slog.Warn("droit de groupe révoqué par le document",
			slog.String("groupe", c.Group), slog.String("environnement", c.Environment))
	}
	for _, c := range plan.Groupes.Accorder {
		slog.Warn("droit de groupe accordé par le document",
			slog.String("groupe", c.Group), slog.String("environnement", c.Environment))
	}
	for _, g := range plan.Groupes.RevoquerAdmin {
		slog.Warn("administration retirée à un groupe par le document", slog.String("groupe", g))
	}
	for _, g := range plan.Groupes.AccorderAdmin {
		slog.Warn("administration accordée à un groupe par le document", slog.String("groupe", g))
	}
	slog.Info("droits réconciliés",
		slog.Int("accordés", len(plan.Accorder)), slog.Int("révoqués", len(plan.Revoquer)),
		slog.Int("inchangés", plan.Inchanges),
		slog.Int("groupes_accordés", len(plan.Groupes.Accorder)+len(plan.Groupes.AccorderAdmin)),
		slog.Int("groupes_révoqués", len(plan.Groupes.Revoquer)+len(plan.Groupes.RevoquerAdmin)))
}

// IntervalleDesProteges est le rythme de relecture du fichier des options par
// environnement.
//
// Aligné sur celui des DROITS (dix secondes), pas sur celui des pods : depuis
// l'ADR-0044, ce document ne porte plus seulement le badge — `allowRun` est
// un gel qu'un exploitant pose pendant un incident, et ce que l'intervalle
// borne est le délai de sa PRISE D'EFFET, la même urgence qu'une révocation
// de droit.
const IntervalleDesProteges = 10 * time.Second

// boucleDesProteges relit le fichier et réconcilie, jusqu'à l'annulation.
//
// Même charpente que boucleDesDroits, et pour la même raison : sans source
// configurée, un déploiement qui ne protège aucun environnement reste valide.
func boucleDesProteges(ctx context.Context, source *protectedfile.Source, st *store.Store, intervalle time.Duration) error {
	if source == nil {
		<-ctx.Done()
		return ctx.Err()
	}
	if intervalle <= 0 {
		intervalle = IntervalleDesProteges
	}

	t := time.NewTicker(intervalle)
	defer t.Stop()

	for {
		reconcilierLesProteges(ctx, source, st)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

// reconcilierLesProteges applique le document des environnements protégés
// s'il a changé.
//
// Aucune erreur n'est REMONTÉE, même raison que reconcilierLesDroits : un
// YAML cassé ici ne doit arrêter ni la réconciliation des pods ni celle des
// droits, qui n'ont rien à voir.
func reconcilierLesProteges(ctx context.Context, source *protectedfile.Source, st *store.Store) {
	if source == nil {
		return
	}

	doc, change, err := source.Lire()
	switch {
	case errors.Is(err, protectedfile.ErrAbsent):
		// NE RIEN FAIRE : un contrôleur démarré avant que le ConfigMap soit posé
		// ne doit pas conclure « rien n'est protégé » et retirer le badge de
		// tout le monde.
		return
	case err != nil:
		slog.Error("document des environnements protégés illisible",
			slog.String("chemin", source.Chemin()), slog.String("error", err.Error()))
		return
	case !change:
		return
	}

	options, err := core.ValiderLesProteges(doc)
	if err != nil {
		slog.Error("document des environnements protégés refusé",
			slog.String("chemin", source.Chemin()), slog.String("error", err.Error()))
		return
	}

	if err := st.ReplaceOptions(ctx, options); err != nil {
		source.Oublier()
		slog.Error("application des environnements protégés en échec",
			slog.String("chemin", source.Chemin()), slog.String("error", err.Error()))
		return
	}

	var geles []string
	for _, o := range options {
		if !o.AllowRun {
			geles = append(geles, o.Environment)
		}
	}
	if len(geles) > 0 {
		slog.Warn("environnements gelés par le document", slog.Any("environnements", geles))
	}

	slog.Info("options par environnement réconciliées", slog.Int("total", len(options)))
}

func valeurOuDefaut(nom, defaut string) string {
	if v := os.Getenv(nom); v != "" {
		return v
	}
	return defaut
}

// secretsDeTirage lit EnvRunnerPullSecrets, une liste séparée par des virgules.
//
// Les entrées vides sont écartées plutôt que transmises : une virgule en trop
// dans un values.yaml produirait sinon un `imagePullSecrets` nommant le Secret
// "", et le pod échouerait au tirage pour une raison sans rapport avec l'image.
func secretsDeTirage() []string {
	brut := os.Getenv(EnvRunnerPullSecrets)
	if brut == "" {
		return nil
	}
	var noms []string
	for _, part := range strings.Split(brut, ",") {
		if n := strings.TrimSpace(part); n != "" {
			noms = append(noms, n)
		}
	}
	return noms
}

func runAPI(ctx context.Context) error {
	dsn, err := databaseURL(EnvDatabaseURL)
	if err != nil {
		return err
	}
	m, err := lireModes()
	if err != nil {
		return err
	}

	// api.FromEnv et non une construction à la main : elle lit
	// WARDEN_INSECURE_DEV_AUTH, sans quoi l'API assemblée refuse TOUTE requête —
	// son middleware répond 501 tant qu'aucun authentificateur n'est configuré,
	// et c'est délibéré de sa part. La construction à la main l'ignorait, donc le
	// binaire déployé était inutilisable et `FromEnv` était du code mort.
	cfg, err := api.FromEnv()
	if err != nil {
		return err
	}

	// Assemblage : c'est le SEUL endroit qui connaît à la fois les ports du
	// cœur et les adaptateurs qui les satisfont (ADR-0019).
	st, err := store.Open(ctx, dsn)
	if err != nil {
		return fmt.Errorf("opening store: %w", err)
	}
	defer st.Close()

	// Le lecteur de diffs a sa propre identité de connexion : les payloads
	// --diff sont sous contrôle d'accès distinct (ADR-0008). À défaut de DSN
	// dédié, on retombe sur le DSN applicatif — acceptable en développement,
	// pas en production.
	diffDSN := os.Getenv(EnvDiffDatabaseURL)
	if diffDSN == "" {
		diffDSN = dsn
	}
	dr, err := store.OpenDiffReader(ctx, diffDSN)
	if err != nil {
		return fmt.Errorf("opening diff reader: %w", err)
	}
	defer dr.Close()

	f, err := flux.Open(ctx, dsn)
	if err != nil {
		return fmt.Errorf("opening flux: %w", err)
	}
	defer f.Close()

	// LISTEN/NOTIFY appartient à l'adaptateur, pas au serveur HTTP.
	go func() { _ = f.Listen(context.WithoutCancel(ctx)) }()

	// Le client vers le rôle `git` est FACULTATIF : sans lui, les diffs git
	// répondent 501 et le reste de l'API fonctionne. C'est ce qui permet de
	// développer sans volume de dépôts.
	//
	// Le type est celui du PORT et non *gitclient.Client : une valeur nulle de
	// type concret rangée dans un champ d'interface n'est pas nil, et la branche
	// qui répond 501 se transformerait en déréférencement de pointeur nul.
	var (
		dispatcher   *dispatch.Dispatcher
		dispatchAddr string
		starter      core.RunLauncher
		keys         core.KeyIssuer
		keyStore     core.KeyStore
		vault        core.VaultIssuer
		vaultStore   core.VaultStore
		consigne     core.SecretStash

		repo    core.RepoDiffer
		mirrors core.MirrorRefresher
		refs    core.RefResolver
		projet  core.ProjectConfigReader
		makers  core.RepoMaterializer
		grants  core.PushGrantIssuer
		creator core.RunCreator
		audits  core.AuditRecorder
	)
	if adresse := os.Getenv(EnvGitInternalURL); adresse != "" {
		jeton := os.Getenv(EnvGitInternalToken)
		if jeton == "" {
			// Une URL sans jeton n'est pas une dégradation, c'est une erreur de
			// configuration : elle produirait des 401 en silence à la première
			// consultation d'audit.
			return fmt.Errorf("%s est définie sans %s", EnvGitInternalURL, EnvGitInternalToken)
		}
		gc, err := gitclient.New(gitclient.Config{BaseURL: adresse, Token: jeton})
		if err != nil {
			return fmt.Errorf("client du rôle git: %w", err)
		}
		repo, mirrors, refs, makers, audits = gc, gc, gc, gc, gc
		// La configuration de projet suit le MÊME client : elle est lue dans
		// l'arbre du commit par le rôle `git` (ADR-0009). Sans rôle `git`
		// configuré, elle reste nil et `core.ProjectConfigFor` rend une
		// configuration vide — les runs partent comme avant.
		projet = gc

		// Le frappeur de jetons est construit à part du client HTTP : signer ne
		// fait AUCUN appel réseau, et les mêler donnerait à une opération
		// purement locale l'apparence de dépendre de la disponibilité du rôle
		// `git` (ADR-0028).
		gi, err := gitclient.NewGrantIssuer(jeton)
		if err != nil {
			return fmt.Errorf("frappeur de jetons de push: %w", err)
		}
		grants = gi

		// La création de run n'est branchée QUE si le rôle `git` est joignable.
		// Sans lui, `POST /runs` ne peut ni rafraîchir le miroir ni résoudre la
		// ref d'audit, et enregistrerait donc un run dont l'artefact est
		// introuvable — exactement ce que la résolution côté serveur existe pour
		// empêcher.
		creator = st

		// Le démarrage des runs n'est branché que si le produit a de quoi lancer
		// un runner : le cluster, ou Docker quand il est choisi explicitement
		// (ADR-0047). Sans lui, `POST /runs` ENREGISTRE le run sans le démarrer
		// et le dit : un déploiement sans droits Kubernetes reste utilisable pour
		// tout le reste, ce qui est le mode de développement.
		ns := os.Getenv(EnvPodNamespace)
		declencheur := EnvPodNamespace
		if m.orchestrateur == modeDocker {
			declencheur = EnvOrchestrator + "=" + modeDocker
		}
		if m.orchestrateur == modeDocker || ns != "" {
			image := os.Getenv(EnvRunnerImage)
			if image == "" {
				// L'ADR-0014 fait déclarer l'image par le projet ; cette
				// configuration n'existe pas encore, donc elle vient du
				// déploiement. Son absence produirait des runners qui échouent
				// au démarrage, loin de la cause.
				return fmt.Errorf("%s est définie sans %s", declencheur, EnvRunnerImage)
			}
			// L'adresse que le RUNNER joindra, distincte de celle sur laquelle le
			// service écoute : un runner n'est pas dans le même espace réseau que
			// le processus qui sert. Sans elle, les runners démarrent et
			// s'arrêtent faute de savoir qui appeler.
			dispatchURL := os.Getenv(EnvDispatchRunnerURL)
			if dispatchURL == "" {
				return fmt.Errorf("%s est définie sans %s", declencheur, EnvDispatchRunnerURL)
			}
			// Même raisonnement que pour le dispatch, avec une conséquence plus
			// dure : un runner sans adresse d'ingestion exécuterait son playbook
			// sans pouvoir rien rapporter, et l'audit ne dirait rien de ce qui
			// a été modifié — l'inverse exact du produit (§6.3).
			ingestURL := os.Getenv(EnvIngestRunnerURL)
			if ingestURL == "" {
				return fmt.Errorf("%s est définie sans %s", declencheur, EnvIngestRunnerURL)
			}
			// Le plafond de durée d'un run vient du DÉPLOIEMENT, faute d'une
			// configuration projet (ADR-0014). C'est la décision actée n°13 : ce
			// qui borne un run qui pend même contrôleur mort, donc la seule
			// protection qui ne dépend d'aucun de nos processus — en mode
			// Kubernetes du moins ; en mode Docker, c'est le contrôleur qui la
			// porte (ADR-0047). Absente, la valeur par défaut de l'adaptateur
			// s'applique.
			deadline, err := secondesDeDeadline()
			if err != nil {
				return err
			}

			var (
				launcher core.PodLauncher
				orch     *orchestrator.Kubernetes
				k8s      *kubernetes.Clientset
			)
			switch m.orchestrateur {
			case modeDocker:
				d, err := lanceurDocker(dispatchURL, ingestURL, deadline)
				if err != nil {
					return err
				}
				launcher = d
			default:
				k8s, err = clientKubernetes()
				if err != nil {
					return err
				}
				orch, err = orchestrator.New(k8s, orchestrator.Config{
					Namespace: ns, DispatchURL: dispatchURL, IngestURL: ingestURL,
					DeadlineSeconds: deadline,
					PullSecrets:     secretsDeTirage(),
				})
				if err != nil {
					return err
				}
				launcher = orch
			}
			rs, err := core.NewRunStarter(st, launcher, gc, image)
			if err != nil {
				return err
			}
			starter = rs

			switch m.secrets {
			case modeVault:
				// Les trois magasins dans Vault (ADR-0047) : clés d'accès, clé
				// de vault, et consigne des valeurs de passage.
				mv, err := ouvrirVault(st)
				if err != nil {
					return err
				}
				keys, keyStore = mv.cles, mv.cles
				vault, vaultStore = mv.vault, mv.vault
				consigne = mv.consigne
			default:
				// Le magasin de clés partage la condition du cluster, et pas par
				// commodité : la partie privée vit dans un Secret du namespace
				// des pods (ADR-0020). Sans accès à Kubernetes il n'y a pas de
				// magasin, donc pas de clé — et les ordres partent sans, ce que
				// le composeur traite comme un état légitime.
				ki, err := keyinventory.New(k8s, ns, st)
				if err != nil {
					return err
				}
				keys, keyStore = ki, ki

				// La clé de vault (ADR-0045) partage la même condition que la
				// clé SSH : sa valeur en clair vit dans un Secret du namespace
				// des pods.
				vi, err := vaultinventory.New(k8s, ns, st)
				if err != nil {
					return err
				}
				vault, vaultStore = vi, vi

				// L'orchestrateur tient déjà le cycle de vie des Secrets d'un
				// pod ; la consigne des valeurs de passage y vit pour la même
				// raison.
				consigne = orch
			}
		}

		// Le canal de dispatch (ADR-0018) n'est monté que si une adresse lui est
		// donnée. Son composeur d'ordres a besoin du frappeur de jetons — c'est
		// lui qui met le jeton de lecture de l'ADR-0007 dans la main du runner —
		// donc il partage la même condition que le reste.
		if adresse := os.Getenv(EnvDispatchAddr); adresse != "" {
			origine := os.Getenv(EnvGitRunnerURL)
			if origine == "" {
				// L'ordre partirait avec une origine vide, et le runner échouerait
				// sur un message de git parlant d'URL malformée — très loin de la
				// cause. C'est une DEUXIÈME adresse du rôle `git`, distincte de
				// celle de la CLI : lecture et écriture sont deux ports (ADR-0027).
				return fmt.Errorf("%s est définie sans %s", EnvDispatchAddr, EnvGitRunnerURL)
			}
			ob, err := core.NewOrderBuilder(st, gi, keys, origine)
			if err != nil {
				return fmt.Errorf("composeur d'ordres: %w", err)
			}
			// L'autorisation est revérifiée à la LIVRAISON de la clé, et c'est ce
			// qui donne un sens à la révocation (§10.3). Sans ce branchement, le
			// composeur refuserait toute clé — c'est le défaut voulu.
			ob = ob.AvecDroits(st)
			// Les empreintes des machines déclarées, vérifiées par chaque run
			// (ADR-0048). Sans elles, la vérification resterait stricte sur une
			// liste vide et aucune machine ne serait joignable.
			ob = ob.AvecMachines(st)
			switch v := os.Getenv(EnvHostKeyChecking); v {
			case "", "strict":
			case "off":
				slog.Warn("empreintes non vérifiées pour les runs à clé",
					slog.String("variable", EnvHostKeyChecking),
					slog.String("effet", "un nœud usurpé reçoit la connexion ; les runs à mot de passe restent vérifiés"))
				ob = ob.SansVerifierLesEmpreintes()
			default:
				return fmt.Errorf("%s=%q : `strict` ou `off` attendu", EnvHostKeyChecking, v)
			}
			// Facultatif comme `keys` : sans magasin de vault, l'ordre part
			// sans clé et le run tourne — il échouera au premier `!vault |`
			// qu'il ne peut pas déchiffrer, ce qui est un diagnostic honnête.
			ob = ob.AvecVault(vault)
			// La consigne sert DEUX chemins : le mot de passe d'un enrôlement et
			// celui d'élévation d'un playbook. Les deux viennent d'un humain et
			// ne font que passer, d'où le même magasin.
			if consigne != nil {
				ob = ob.AvecEnrolement(st, st, consigne)
			}
			dispatcher = dispatch.New(st, ob, st)
			dispatchAddr = adresse
		}
	}

	// L'authentification par mot de passe ne demande QUE PostgreSQL : elle est
	// branchée toujours, sans condition sur le rôle `git` ni sur le cluster.
	// C'est délibéré — un déploiement dégradé doit rester un déploiement où l'on
	// s'authentifie, sans quoi la dégradation ouvrirait la porte.
	auth, err := core.NewAuthenticator(st, st, dureeDeSession())
	if err != nil {
		return err
	}
	if err := brancherLAnnuaire(auth, st); err != nil {
		return err
	}

	server, err := api.New(cfg, api.Deps{
		Runs:          st,
		Users:         st,
		Auth:          auth,
		Accounts:      st,
		Diffs:         dr,
		Events:        f,
		Repo:          repo,
		Repos:         st,
		RepoMaker:     makers,
		Creator:       creator,
		Mirrors:       mirrors,
		Refs:          refs,
		Audits:        audits,
		ProjectConfig: projet,
		// Annoncée à la CLI avec son jeton de push (ADR-0028). Vide, la CLI se
		// rabat sur sa propre variable d'environnement.
		GitPushURL: os.Getenv(EnvGitClientURL),
		// Opt-in EXPLICITE, et le défaut tolère : la bascule arrête les runs de
		// tout dépôt qui n'a pas encore committé son fichier, donc elle doit être
		// un geste d'exploitant et non un effet de bord de mise à jour.
		ExigenceConfig: core.ExigenceConfig(os.Getenv(EnvExigerConfig) == "1"),
		Grants:         grants,
		Starter:        starter,
		Running:        st,
		// Le catalogue et l'inventaire ne demandent que PostgreSQL : ils sont
		// branchés toujours. Le MAGASIN de clés demande le cluster, donc il
		// suit la même condition que le démarrage des runs — sans lui, poser
		// une clé répond 501 et la consulter fonctionne.
		Keys:  st,
		Hosts: st,
		// Les droits par environnement ne demandent que PostgreSQL : branchés
		// toujours, comme l'authentification. Un déploiement dégradé doit rester
		// un déploiement où l'autorisation s'applique.
		Access: st,
		// Les environnements protégés ne demandent que PostgreSQL, comme les
		// droits : branchés toujours, et facultatifs au sens du montage —
		// aucune conséquence de sécurité s'ils manquent, contrairement à
		// `Access` (ADR-0044).
		Protected: st,
		KeyStore:  keyStore,
		// Le catalogue de vault ne demande que PostgreSQL, comme celui des
		// clés SSH ; poser une clé ou chiffrer une valeur demandent le
		// cluster, donc `Vault` suit la même condition que `KeyStore`.
		Vault: vaultStore,
		Stash: consigne,
	})
	if err != nil {
		return fmt.Errorf("creating API server: %w", err)
	}
	defer func() { _ = server.Close() }()

	services := []func(context.Context) error{server.Run}

	if dispatcher != nil {
		services = append(services, func(ctx context.Context) error {
			return servirDispatch(ctx, dispatchAddr, dispatcher)
		})
	}

	// L'ingestion n'a besoin ni du rôle `git` ni du cluster : elle est montée
	// dès qu'une adresse lui est donnée, indépendamment du dispatch. Sa seule
	// dépendance est la résolution `pod -> run`, que le magasin satisfait.
	if adresse := os.Getenv(EnvIngestAddr); adresse != "" {
		h, fermer, err := ouvrirIngestion(ctx, dsn, st)
		if err != nil {
			return err
		}
		defer fermer()
		services = append(services, func(ctx context.Context) error {
			return servirIngestion(ctx, adresse, h)
		})
	}

	if len(services) == 1 {
		return server.Run(ctx)
	}
	return servirTous(ctx, services...)
}

// Variables d'environnement portant les chaînes de connexion.
//
// Deux identités de connexion, donc deux variables (ADR-0008, ADR-0012). Le
// rôle d'ingestion est restreint à INSERT sur les tables d'events ; lui donner
// le DSN applicatif annulerait la séparation sans qu'aucun test ne le voie.
const (
	EnvDatabaseURL       = "WARDEN_DATABASE_URL"
	EnvIngestDatabaseURL = "WARDEN_INGEST_DATABASE_URL"
	EnvDiffDatabaseURL   = "WARDEN_DIFF_DATABASE_URL"
)

// Configuration du rôle `git` et de l'arête qui y mène (ADR-0024).
//
// Deux adresses et non une : le smart HTTP est joignable depuis les pods runner,
// donc depuis du code arbitraire (ADR-0007), là où l'API interne ne l'est que
// par le rôle `api` (ADR-0006). Les séparer dès maintenant rend la NetworkPolicy
// exprimable par port, sans changement de code le jour où le smart HTTP existera.
const (
	EnvGitAddr          = "WARDEN_GIT_ADDR"           // smart HTTP en lecture, servi aux pods runner
	EnvGitInternalAddr  = "WARDEN_GIT_INTERNAL_ADDR"  // API interne, servie par gitd
	EnvGitRoot          = "WARDEN_GIT_ROOT"           // racine des dépôts bare
	EnvGitInternalURL   = "WARDEN_GIT_INTERNAL_URL"   // côté `api` : où joindre gitd
	EnvGitInternalToken = "WARDEN_GIT_INTERNAL_TOKEN" // partagé par les deux, et clé de signature des jetons de push
	EnvGitPushAddr      = "WARDEN_GIT_PUSH_ADDR"      // smart HTTP en écriture, servi à la CLI
	EnvDispatchAddr     = "WARDEN_DISPATCH_ADDR"      // canal de dispatch SSE, servi aux pods runner
	EnvIngestAddr       = "WARDEN_INGEST_ADDR"        // ingestion des events, servie aux pods runner
	EnvGitRunnerURL     = "WARDEN_GIT_RUNNER_URL"     // origine remise au runner : le port de LECTURE de `git`
	EnvGitClientURL     = "WARDEN_GIT_CLIENT_URL"     // adresse ANNONCÉE à la CLI avec son jeton de push : le port d'ÉCRITURE
	EnvExigerConfig     = "WARDEN_EXIGER_CONFIG"      // "1" refuse les commits sans .ansibleproxy.yml (ADR-0009)
	// EnvHostKeyChecking : `strict` (défaut) ou `off`. Interrupteur de
	// TRANSITION, le temps de déclarer les clés d'hôte d'une flotte : `off`
	// coupe la vérification des empreintes des runs à clé, jamais celle d'un
	// run à mot de passe (ADR-0048).
	EnvHostKeyChecking = "WARDEN_HOST_KEY_CHECKING"
	// EnvDispatchRunnerURL est l'adresse du canal telle qu'un POD la joint. Comme
	// pour git, elle diffère de l'adresse d'écoute : le service écoute dans le
	// control plane, le pod appelle depuis le cluster.
	EnvDispatchRunnerURL = "WARDEN_DISPATCH_RUNNER_URL"
	// EnvIngestRunnerURL est l'adresse de l'ingestion telle qu'un POD la joint,
	// distincte de l'adresse d'écoute pour la même raison.
	EnvIngestRunnerURL = "WARDEN_INGEST_RUNNER_URL"
	EnvPodNamespace    = "WARDEN_POD_NAMESPACE" // namespace où vivent les pods runner
	EnvRunnerImage     = "WARDEN_RUNNER_IMAGE"  // image du runner, en attendant la configuration projet (ADR-0014)

	// EnvRunnerPullSecrets nomme les Secrets d'authentification au registre,
	// séparés par des virgules.
	//
	// Elle existe parce que le pod runner n'est PAS créé par la chart : un
	// `imagePullSecrets` posé sur les Deployments ne l'atteint pas. La chart
	// dérive cette variable de la MÊME valeur qui alimente ses propres pods, pour
	// qu'on ne puisse pas authentifier le control plane en oubliant le runner.
	//
	// Absente sur un registre public.
	EnvRunnerPullSecrets = "WARDEN_RUNNER_PULL_SECRETS"

	// EnvRunDeadlineSeconds borne la durée d'un run, en SECONDES. C'est la
	// décision actée n°13 : `activeDeadlineSeconds` sur le pod arrête un run qui
	// pend même si le contrôleur est mort, donc c'est la seule protection qui ne
	// dépend d'aucun de nos processus. Absente, le défaut de l'orchestrateur
	// s'applique — deux heures.
	EnvRunDeadlineSeconds = "WARDEN_RUN_DEADLINE_SECONDS"

	// EnvSessionTTL est la validité d'une session `warden login`, en HEURES.
	// Absente, le défaut du cœur s'applique — trente jours. Voir
	// `core.DureeSession` pour l'arbitrage qu'il représente.
	EnvSessionTTL = "WARDEN_SESSION_TTL_HOURS"

	// EnvLDAPURL branche la connexion par Active Directory (ADR-0053) :
	// `ldaps://dc.exemple.local:636`. Absente, seuls les comptes locaux
	// existent.
	EnvLDAPURL = "WARDEN_LDAP_URL"
	// EnvLDAPStartTLS vaut `1` pour une URL `ldap://` chiffrée par StartTLS.
	EnvLDAPStartTLS = "WARDEN_LDAP_STARTTLS"
	// EnvLDAPBaseDN borne la recherche des comptes et des groupes.
	EnvLDAPBaseDN = "WARDEN_LDAP_BASE_DN"
	// EnvLDAPBindDN et EnvLDAPBindPassword sont le compte de service, en
	// lecture seule, qui cherche les comptes et leurs groupes.
	EnvLDAPBindDN       = "WARDEN_LDAP_BIND_DN"
	EnvLDAPBindPassword = "WARDEN_LDAP_BIND_PASSWORD" // #nosec G101 -- le NOM de la variable, pas le secret
	// EnvLDAPCAFile est l'autorité des contrôleurs de domaine, au format PEM.
	EnvLDAPCAFile = "WARDEN_LDAP_CA_FILE"
	// EnvLDAPSessionTTL est la validité d'une session AD, en HEURES. Défaut
	// dix : c'est la borne de prise d'effet d'un retrait de groupe.
	EnvLDAPSessionTTL = "WARDEN_LDAP_SESSION_TTL_HOURS"

	// EnvReconcileSeconds est le rythme du réconciliateur de PODS, en secondes.
	// Chaque passage coûte un LIST à l'API Kubernetes ; ce qu'il borne est un
	// retard d'audit, pas de sécurité.
	EnvReconcileSeconds = "WARDEN_RECONCILE_SECONDS"

	// EnvGrantsSeconds est le rythme de relecture des DROITS, en secondes.
	// Distinct du précédent : un passage à vide ne coûte qu'une lecture de
	// fichier, et ce qu'il borne est le délai de prise d'effet d'une révocation.
	EnvGrantsSeconds = "WARDEN_GRANTS_SECONDS"

	// EnvGrantsFile est le document des droits par environnement (ADR-0033),
	// monté depuis un ConfigMap versionné dans le dépôt de déploiement. C'est la
	// SEULE source : aucune commande ne modifie les droits.
	//
	// Absente, le contrôleur ne réconcilie aucun droit — et n'en révoque aucun.
	EnvGrantsFile = "WARDEN_GRANTS_FILE"

	// EnvProtectedSeconds est le rythme de relecture des options par
	// environnement, en secondes (ADR-0044). Aligné sur celui des droits :
	// `allowRun` est un gel dont la prise d'effet est aussi urgente qu'une
	// révocation.
	EnvProtectedSeconds = "WARDEN_PROTECTED_SECONDS"

	// EnvProtectedFile est le document des options par environnement
	// (ADR-0044) — badge visuel et gel de lancement — monté depuis un
	// ConfigMap versionné distinct de celui des droits : ces notions sont
	// séparées, l'une contrôle un accès à un credential, les deux autres non.
	//
	// Absente, le contrôleur ne déclare aucune option par environnement — et
	// n'en retire aucune : aucun badge, aucun gel.
	EnvProtectedFile = "WARDEN_PROTECTED_FILE"

	// EnvInsecureKubeconfig autorise le kubeconfig local au lieu de la
	// configuration in-cluster. Développement et tests d'étage 3 UNIQUEMENT : il
	// laisse le control plane lancer des pods dans le cluster que kubectl vise,
	// qui n'est pas forcément celui auquel cette base d'audit correspond.
	EnvInsecureKubeconfig = "WARDEN_INSECURE_KUBECONFIG"
	EnvForgeToken         = "WARDEN_FORGE_TOKEN" // lecture sur la forge, pour le miroir de la principale
)

// HookName est le nom sous lequel git invoque ce binaire comme hook.
//
// Le rôle `git` installe un lien symbolique portant ce nom vers son propre
// exécutable (ADR-0027). Le dispatch se fait sur argv[0] : pas de script shell,
// donc pas d'interpréteur à supposer présent dans l'image ni de citation à rater
// entre nous et un contrôle de sécurité.
const HookName = "pre-receive"

// PreReceiveHook exécute le hook et rend le code de sortie du processus.
//
// Exposé ici parce que `cmd/wardend` ne peut atteindre que ce paquet
// (ADR-0011) : il n'a pas le droit d'importer `internal/server/internal/gitd`,
// et c'est bien ainsi.
func PreReceiveHook(ctx context.Context, stdin io.Reader, stderr io.Writer) int {
	return gitd.PreReceive(ctx, stdin, stderr)
}

// Migrate applique les migrations de schéma en attente.
//
// Sous-commande dédiée, et non un traitement au démarrage d'un pod : le rôle
// `api` tourne à N réplicas, migrer au démarrage ferait démarrer N migrateurs
// concurrents (ADR-0012). Elle est lancée par un Job Helm en pre-upgrade.
func Migrate(ctx context.Context) error {
	dsn, err := databaseURL(EnvDatabaseURL)
	if err != nil {
		return err
	}
	return store.Migrate(ctx, dsn)
}

// databaseURL lit une chaîne de connexion et échoue tôt si elle manque.
//
// Un secret absent doit arrêter le démarrage, pas produire une erreur de
// connexion trois couches plus bas au premier appel. La valeur n'est jamais
// journalisée ni incluse dans un message d'erreur : elle porte le mot de passe.
func databaseURL(name string) (string, error) {
	dsn := os.Getenv(name)
	if dsn == "" {
		return "", fmt.Errorf("%s n'est pas définie", name)
	}
	return dsn, nil
}

// runGit sert l'API interne du rôle `git` (ADR-0024) et ses deux smart HTTP :
// la LECTURE aux pods runner (ADR-0007) et l'ÉCRITURE à la CLI (ADR-0027).
//
// Trois surfaces, trois ports d'écoute. La séparation est portée par le PORT et
// non par le chemin, ce qui la rend exprimable en NetworkPolicy — et pour
// l'écriture, c'est le contrôle n°3 de la conjonction de l'ADR-0027.
// clientKubernetes construit le client Kubernetes.
//
// In-cluster PAR DÉFAUT, et c'est ce qui compte : le control plane tourne dans le
// cluster qu'il pilote, et accepter un kubeconfig sans le dire ouvrirait la
// possibilité de lancer des pods ailleurs — dans un cluster que personne n'a
// relié à cette base d'audit.
//
// Le kubeconfig reste atteignable par un opt-in EXPLICITE, nommé pour que
// personne ne le pose par accident. Même forme que WARDEN_INSECURE_DEV_AUTH, et
// pour la même raison : sans lui le produit n'est ni testable ni exploitable hors
// d'un cluster, ce qui pousse à contourner la règle plutôt qu'à la respecter.
func clientKubernetes() (*kubernetes.Clientset, error) {
	if os.Getenv(EnvInsecureKubeconfig) == "1" {
		regles := clientcmd.NewDefaultClientConfigLoadingRules()
		cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			regles, &clientcmd.ConfigOverrides{}).ClientConfig()
		if err != nil {
			return nil, fmt.Errorf("kubeconfig: %w", err)
		}
		client, err := kubernetes.NewForConfig(cfg)
		if err != nil {
			return nil, fmt.Errorf("client Kubernetes: %w", err)
		}
		return client, nil
	}

	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("configuration Kubernetes in-cluster: %w "+
			"(hors cluster, poser %s=1)", err, EnvInsecureKubeconfig)
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("client Kubernetes: %w", err)
	}
	return client, nil
}

// servirIngestion sert l'endpoint d'ingestion sur son propre port.
//
// Un port DISTINCT de celui de l'API, avec sa propre règle d'ingress et son
// propre rôle PostgreSQL (ADR-0008). Quand l'ADR-0007 dit que le runner ne
// joint jamais l'API, cela se lit « jamais le port de l'API ».
//
// Le service est monté à partir d'un pool d'ingestion, pas du pool applicatif :
// c'est la seconde couche d'isolation, celle qui limite les dégâts si la
// première — la résolution `pod -> run` côté serveur — cédait.
func servirIngestion(ctx context.Context, addr string, h *ingest.Handler) error {
	srv := &http.Server{
		Addr:    addr,
		Handler: h.Routes(),
		// Sans délai sur la lecture des en-têtes, une connexion qui les envoie
		// octet par octet immobilise un handler indéfiniment (Slowloris).
		ReadHeaderTimeout: 10 * time.Second,
		// WriteTimeout ici, contrairement au dispatch : une réponse d'ingestion
		// est un accusé de quelques octets, jamais un flux. Un handler qui
		// mettrait plus longtemps est bloqué, pas occupé.
		WriteTimeout: 30 * time.Second,
	}

	var lc net.ListenConfig
	listener, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("écoute de l'ingestion sur %s: %w", addr, err)
	}

	// #nosec G118 -- l'arrêt gracieux part d'un contexte neuf par nécessité :
	// celui-ci vient d'être annulé, et c'est ce qui déclenche l'arrêt.
	go func() {
		<-ctx.Done()
		arret, annuler := context.WithTimeout(context.Background(), 30*time.Second)
		defer annuler()
		_ = srv.Shutdown(arret)
	}()

	if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("service d'ingestion: %w", err)
	}
	return nil
}

// ouvrirIngestion monte le handler d'ingestion sur son pool restreint.
//
// Le DSN d'ingestion porte le rôle `warden_ingest`, restreint à l'insertion
// d'events et aux cinq colonnes de statut d'un run (ADR-0008, migration 00006).
// À défaut, on retombe sur le DSN applicatif — acceptable en développement,
// jamais en production, et c'est exactement le repli déjà consenti au lecteur
// de diffs.
func ouvrirIngestion(ctx context.Context, dsnApp string, pods ingest.Pods) (*ingest.Handler, func(), error) {
	dsn := os.Getenv(EnvIngestDatabaseURL)
	if dsn == "" {
		slog.Warn("ingestion sur le DSN applicatif",
			slog.String("variable", EnvIngestDatabaseURL),
			slog.String("effet", "le rôle restreint de l'ADR-0008 n'est pas en vigueur"))
		dsn = dsnApp
	}

	in, err := store.OpenIngest(ctx, dsn)
	if err != nil {
		return nil, nil, fmt.Errorf("ouverture du pool d'ingestion: %w", err)
	}

	h, err := ingest.New(pods, in)
	if err != nil {
		in.Close()
		return nil, nil, err
	}
	return h, in.Close, nil
}

// servirDispatch sert le canal de dispatch sur son propre port.
//
// Un port DISTINCT de celui de l'API et de celui de l'ingestion, chacun avec sa
// règle d'ingress. C'est la troisième surface joignable depuis du code arbitraire
// (§2), et la seule qui distribue des credentials : la séparer par le port et non
// par le chemin est ce qui la rend restreignable sans changement de code.
func servirDispatch(ctx context.Context, addr string, d *dispatch.Dispatcher) error {
	srv := &http.Server{
		Addr:    addr,
		Handler: d,
		// Sans délai sur la lecture des en-têtes, une connexion qui les envoie
		// octet par octet immobilise un handler indéfiniment (Slowloris).
		ReadHeaderTimeout: 10 * time.Second,
		// Pas de WriteTimeout : SSE est un flux qui dure toute la vie du pod, et
		// un délai d'écriture le couperait au milieu d'un run.
	}

	var lc net.ListenConfig
	listener, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("écoute du dispatch sur %s: %w", addr, err)
	}

	// #nosec G118 -- l'arrêt gracieux part d'un contexte neuf par nécessité :
	// celui-ci vient d'être annulé, et c'est ce qui déclenche l'arrêt.
	go func() {
		<-ctx.Done()
		arret, annuler := context.WithTimeout(context.Background(), 30*time.Second)
		defer annuler()
		_ = srv.Shutdown(arret)
	}()

	if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("service de dispatch: %w", err)
	}
	return nil
}

// servirTous fait tourner N services et rend la première erreur.
//
// La première erreur fait tomber les autres : continuer à distribuer des ordres
// alors que l'API est morte laisserait des runs démarrer sans que personne
// puisse les suivre. Le raisonnement vaut a fortiori pour l'ingestion — un run
// qui tourne et dont l'audit ne s'écrit pas est ce que le §6.3 interdit.
func servirTous(ctx context.Context, services ...func(context.Context) error) error {
	ctx, annuler := context.WithCancel(ctx)
	defer annuler()

	erreurs := make(chan error, len(services))
	for _, servir := range services {
		go func() { erreurs <- servir(ctx) }()
	}

	premiere := <-erreurs
	annuler()
	// Les autres sont attendus : sortir sans les attendre laisserait des
	// listeners ouverts sur des ports que le processus suivant voudra prendre.
	for range len(services) - 1 {
		<-erreurs
	}
	return premiere
}

func runGit(ctx context.Context) error {
	// ADR-0005 : le moteur est le binaire git, et sa version est une condition
	// de correction, pas une préférence. Le contrôle est ici, au démarrage du
	// processus, plutôt que dans gitd.New — c'est le processus qui a une version
	// de git, pas l'objet.
	if err := git.CheckVersion(ctx); err != nil {
		return fmt.Errorf("rôle git: %w", err)
	}

	cfg := gitd.Config{
		Addr:             os.Getenv(EnvGitInternalAddr),
		Racine:           os.Getenv(EnvGitRoot),
		Jeton:            os.Getenv(EnvGitInternalToken),
		ForgeToken:       os.Getenv(EnvForgeToken),
		GracefulShutdown: 30 * time.Second,
	}

	// La LECTURE, servie aux pods runner (ADR-0007). Son port d'autorisation
	// vérifie le jeton de run, signé du même secret et d'un GENRE distinct de
	// celui de l'écriture — un pod runner porte du code arbitraire et ne doit pas
	// pouvoir présenter son jeton comme une autorisation d'écrire.
	if adresse := os.Getenv(EnvGitAddr); adresse != "" {
		authz, err := gitd.NewFetchAuthorizer(cfg.Jeton)
		if err != nil {
			return fmt.Errorf("rôle git: %w", err)
		}
		cfg.SmartAddr = adresse
		cfg.Authorizer = authz
	}

	// L'écriture n'est servie que si une adresse lui est donnée. Son port
	// d'autorisation vérifie des jetons signés avec le secret interne
	// (ADR-0028) : pas d'état, donc rien à provisionner ni à purger.
	if adresse := os.Getenv(EnvGitPushAddr); adresse != "" {
		authz, err := gitd.NewGrantAuthorizer(cfg.Jeton)
		if err != nil {
			return fmt.Errorf("rôle git: %w", err)
		}
		cfg.PushAddr = adresse
		cfg.PushAuthorizer = authz
	}

	srv, err := gitd.New(cfg)
	if err != nil {
		return fmt.Errorf("rôle git: %w", err)
	}
	return srv.Run(ctx)
}

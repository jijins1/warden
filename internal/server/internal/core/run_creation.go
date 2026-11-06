package core

import (
	"context"
	"errors"
	"regexp"
	"time"
)

// NewRun porte ce qu'on connaît d'un run au moment de l'insérer, c'est-à-dire
// AVANT que le pod existe.
//
// On y trouve ce que git ne conserve PAS (ADR-0021) : le contenu à un SHA est
// figé par git et n'a pas à être recopié ici, mais la position d'une branche à
// une date ne l'est pas — les reflogs expirent et sont locaux au miroir.
type NewRun struct {
	// RunRef est un ULID, sûr comme nom de ref git (ADR-0007) et dans une URL.
	// C'est l'identifiant qui sort du produit ; l'id bigint reste interne.
	RunRef string
	UserID int64

	// Repo est le dépôt d'où vient le run. Le rôle `git` en déduit le bare repo
	// à ouvrir : l'ADR-0006 veut tous les runs d'un dépôt dans le même, et les
	// diffs du §6.5 en dépendent.
	Repo string

	Environment string
	Inventory   string
	Playbook    string

	// Kind vaut `playbook` quand il est vide : c'est le genre de tout ce que la
	// CLI crée par `warden submit`.
	Kind RunKind

	// EnrollTarget est exigée pour un enrôlement, et refusée sinon. Elle ne
	// porte JAMAIS le mot de passe.
	EnrollTarget *EnrollTarget

	// AdhocTarget est exigée pour un run ad-hoc, et refusée sinon.
	AdhocTarget *AdhocTarget

	AuditSHA string // le commit d'audit poussé par la CLI (§5.2)
	BaseSHA  string // son parent (ADR-0003)

	// MainlineRef et MainlineSHA figent le point de comparaison. Sans eux, le
	// diff « run contre branche principale » du §6.5 se calcule contre la
	// branche telle qu'elle est AUJOURD'HUI, et change donc à chaque
	// consultation. Le nom de la ref accompagne le SHA parce que la branche de
	// référence est une configuration par environnement (§3.2) : un SHA seul
	// cesse d'être interprétable si elle change.
	MainlineRef string
	MainlineSHA string

	// Flags tels qu'exécutés (§5.5). Il n'y a pas de colonne « demandé » en
	// face, et ce n'est plus tout à fait pour la raison que l'ADR-0021 donnait :
	// l'allowlist existe désormais (ADR-0038), mais elle REFUSE au lieu de
	// filtrer. Une requête portant un drapeau non accepté n'est pas amputée, elle
	// est rejetée — donc ce qui est enregistré est bien ce qui a été demandé.
	//
	// Une liste de chaînes DÉJÀ validée par `ValiderFlags`, et non le
	// `json.RawMessage` du corps de la requête : ces valeurs finissent sur
	// l'`argv` d'`ansible-playbook`, et laisser du JSON brut traverser le cœur
	// revenait à ne les regarder nulle part.
	Flags []string

	RunnerImage       string // ADR-0014
	RunnerImageDigest string

	// ScheduleID désigne le cron qui a déclenché le run (ADR-0057), nil pour un
	// run lancé par une personne. C'est ce qui répond à « qui a lancé ce run à
	// 3 h du matin » : le propriétaire, PAR ce cron.
	ScheduleID *int64
}

// ulid borne l'identifiant public d'un run : Crockford base32 sur 26 caractères,
// sans I, L, O ni U — exclus pour qu'un identifiant recopié à la main ne puisse
// pas être confondu avec 1 et 0.
//
// Il est tiré par la CLI et non par le serveur, ce qui supprime un aller-retour
// (ADR-0028). Il est donc de l'entrée non fiable, et sa forme est vérifiée avant
// tout usage : il compose un nom de ref git et une URL.
var ulid = regexp.MustCompile(`^[0-9ABCDEFGHJKMNPQRSTVWXYZ]{26}$`)

// ValidRunRef vérifie la forme d'un identifiant public de run.
func ValidRunRef(ref string) bool { return ulid.MatchString(ref) }

// RunCreator crée la ligne de run.
//
// Séparé de RunReader : lire un run et en créer un sont deux capacités, et les
// réunir dans un port unique donnerait le droit d'écrire à tout ce qui a besoin
// de lire.
type RunCreator interface {
	CreateRun(ctx context.Context, n NewRun) (int64, error)
}

// ErrRunRefExists signale qu'un run porte DÉJÀ cet identifiant public.
//
// Ce n'est pas une erreur interne, et c'est tout le point : la CLI réutilise
// délibérément le même `run_ref` pour reprendre une soumission ou un enrôlement
// interrompus. Sans erreur distincte, la violation de contrainte remonte en 500
// avec un message qui parle de PostgreSQL, là où l'appelant a besoin de savoir
// ce qu'est devenu le run qu'il croit reprendre.
//
// L'appelant DOIT alors décider en regardant l'état du run existant. Un run
// d'audit ne se réécrit pas : recréer sous le même identifiant effacerait le
// premier enregistrement, qui est précisément ce que le produit existe pour
// conserver.
var ErrRunRefExists = errors.New("un run porte déjà cet identifiant")

// RefResolver rend la position d'une ref dans un dépôt.
//
// Il existe pour une raison de sécurité et non de commodité. `audit_sha` ne peut
// pas venir du client : le diff d'audit du §6.5 compare `mainline_sha` à
// `audit_sha` sans se demander à qui ce dernier appartient, donc un utilisateur
// qui déclarerait celui d'un autre lirait le code de cet autre à travers le diff
// de SON PROPRE run — auquel la politique d'accès lui donne légitimement droit.
//
// Le serveur compose donc la ref lui-même, à partir de l'identité authentifiée et
// de l'identifiant du run, et demande où elle pointe.
type RefResolver interface {
	ResolveRef(ctx context.Context, repo, ref string) (string, error)
}

// AuditRef décrit la ref d'audit que le SERVEUR pointe lui-même sur un commit
// existant, sans qu'aucun client ne pousse (ADR-0042).
type AuditRef struct {
	Repo string
	// Ref est composée par AuditRefFor, jamais par l'adaptateur : savoir à qui
	// appartient une ref d'audit est une règle de run, pas une règle git.
	Ref string
	// CommitSHA vient de MirrorRefresher, jamais d'un client — c'est ce qui
	// dispense ce chemin du contrôle de fraîcheur de l'ADR-0027.
	CommitSHA string
}

// AuditRecorder pointe une ref d'audit sur un commit existant, sans push.
//
// Il existe pour un appelant qui n'a rien à capturer : une page ne lance que la
// branche protégée telle quelle, donc son artefact d'audit est une SECONDE REF
// sur le commit déjà résolu par MirrorRefresher — pas un nouveau commit. Rien en
// aval ne distingue « commit fraîchement frappé » de « commit existant référencé
// une seconde fois ».
//
// SÉPARÉ de RefResolver et de MirrorRefresher bien que le même adaptateur les
// serve tous les trois : lire une position et ÉCRIRE une ref d'audit ne sont pas
// la même capacité, et les réunir donnerait le droit d'écrire à tout ce qui a
// besoin de lire.
//
// Idempotent : un rejeu pour la même ref et le même commit ne rend pas d'erreur.
// Une ref déjà écrite sur un AUTRE commit en rend une — ErrAuditRefConflict —
// et l'appelant doit la distinguer d'une panne.
type AuditRecorder interface {
	RecordAuditRef(ctx context.Context, a AuditRef) error
}

// ErrAuditRefConflict : cette ref d'audit existe et désigne un autre commit.
//
// Un identifiant de run réutilisé sur un autre commit, donc une demande de
// réécrire un enregistrement d'audit — la seule chose qu'un journal d'audit ne
// doit jamais permettre (ADR-0027). Nommée ici parce que l'appelant y répond par
// un 409 et non par un 500 : ce n'est pas une panne.
var ErrAuditRefConflict = errors.New("cette ref d'audit désigne déjà un autre commit")

// AuditRefFor compose la ref d'audit d'un run.
//
// Les deux composants viennent du SERVEUR : l'identifiant numérique de
// l'utilisateur, qui sort de l'authentification, et l'identifiant du run, dont la
// forme est vérifiée juste avant. Aucun des deux n'est cru sur parole, ce qui est
// exactement ce qui rend la résolution sûre.
//
// L'identifiant numérique plutôt que le nom affiché : celui-ci n'est ni stable ni
// unique, et il change quand l'annuaire change. Une ref d'audit vit indéfiniment
// (ADR-0013) ; elle ne peut pas dépendre d'une valeur qui bouge.
func AuditRefFor(userID int64, runRef string) string {
	return "refs/audit/" + itoa(userID) + "/" + runRef
}

// AuditPrefixFor compose le préfixe qu'un jeton de push ouvre à un utilisateur.
//
// La barre finale est ce qui borne le namespace : sans elle, le préfixe de
// l'utilisateur 1 couvrirait aussi celui de l'utilisateur 12.
func AuditPrefixFor(userID int64) string {
	return "refs/audit/" + itoa(userID) + "/"
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// PushGrantIssuer frappe le jeton qui autorise un push d'audit.
//
// Un port, parce que la frappe suppose un secret partagé et un format de fil, et
// que le cœur ne connaît ni l'un ni l'autre (ADR-0019). L'implémentation vit du
// côté sortant, seul autorisé à voir le contrat `gitwire`.
//
// La portée passée ici est DÉRIVÉE de l'identité authentifiée par l'appelant. Une
// implémentation n'a pas à la vérifier — mais elle n'a pas non plus le droit de
// l'élargir.
type PushGrantIssuer interface {
	IssuePushGrant(ctx context.Context, repo, refPrefix string, expires time.Time) (string, error)
}

// RunNamespaceCreator crée la ref que le runner verra comme sa seule ref.
//
// SÉPARÉ de la frappe du jeton, alors que les deux préparent le même accès. Le
// premier jet les réunissait au motif qu'ils « n'ont de sens qu'ensemble » —
// c'était faux, et le compilateur l'a dit : ils sont implémentés par deux
// adaptateurs différents, l'un qui appelle le rôle `git` par le réseau, l'autre
// qui signe localement. Un port qui force un adaptateur à porter une méthode
// dont il n'a que faire n'est plus une couture, c'est une contrainte.
type RunNamespaceCreator interface {
	CreateRunNamespace(ctx context.Context, repo, namespace, sha string) error
}

// FetchGrantIssuer frappe le jeton de run de l'ADR-0007.
//
// Aucune écriture, aucun réseau : signer est une opération locale, et le port le
// dit en ne demandant rien d'autre.
type FetchGrantIssuer interface {
	IssueFetchGrant(ctx context.Context, repo, namespace string, expires time.Time) (string, error)
}

// RunNamespaceFor compose la valeur de GIT_NAMESPACE d'un run.
//
// ATTENTION au chemin réel : git IMBRIQUE chaque composant, donc `runs/<id>`
// résout `refs/namespaces/runs/refs/namespaces/<id>/`. La composition du chemin
// appartient au rôle `git`, qui la fait par `git.NamespacedRef` — l'écrire à la
// main est l'erreur que l'ADR-0026 a mesurée, et elle échoue sans bruit.
func RunNamespaceFor(runRef string) string { return "runs/" + runRef }

package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// ErrAnnuaireIndisponible distingue une panne de l'annuaire d'un refus.
//
// Les deux ne demandent pas la même conduite : un refus se corrige en retapant
// son mot de passe, une panne se corrige côté exploitation. Les confondre ferait
// passer une coupure de l'AD pour une vague de mots de passe faux (ADR-0053).
var ErrAnnuaireIndisponible = errors.New("annuaire indisponible")

// DureeSessionAnnuaire est la validité d'une session ouverte par l'annuaire.
//
// Dix heures, contre trente jours pour un compte local, et l'écart est le point.
// Les groupes sont figés dans la session (ADR-0053, décision 3) : sa durée est
// donc la borne de prise d'effet d'un retrait de groupe dans l'AD. Dix heures
// couvrent une journée de travail sans se reconnecter, et un départ prend effet
// au plus tard le lendemain matin.
const DureeSessionAnnuaire = 10 * time.Hour

// IdentiteAnnuaire est ce que l'annuaire affirme d'un utilisateur dont il vient
// de vérifier le mot de passe.
type IdentiteAnnuaire struct {
	// Subject est l'objectGUID : immuable, contrairement au login (ADR-0021).
	Subject     string
	DisplayName string
	Email       string

	// Groupes sont les noms des groupes, imbriqués compris, NORMALISÉS par
	// NormaliserGroupe. C'est ce que la session retiendra.
	Groupes []string
}

// Annuaire vérifie un mot de passe auprès d'un annuaire d'entreprise.
//
// Deux erreurs ont un sens pour l'appelant : ErrIdentifiantsRefuses — compte
// inconnu, mauvais mot de passe, compte désactivé dans l'AD, sans distinction —
// et ErrAnnuaireIndisponible. Toute autre erreur est traitée comme la seconde.
type Annuaire interface {
	Authentifier(ctx context.Context, login string, motDePasse []byte) (IdentiteAnnuaire, error)
}

// ComptesAnnuaire provisionne les comptes que l'annuaire a authentifiés, et
// dit quels groupes ouvrent l'administration.
//
// `UpsertUser` est celui du magasin : provisionner appartient au flux qui a
// vérifié l'identité, et à lui seul.
type ComptesAnnuaire interface {
	UpsertUser(ctx context.Context, id Identity) (User, error)

	// GroupesAdmin rend, normalisés, les groupes vivants de `admins`.
	GroupesAdmin(ctx context.Context) ([]string, error)
}

// EstAdministrateurParGroupe dit si l'un des groupes d'une session figure
// parmi ceux qui ouvrent l'administration (ADR-0053, décision 5).
//
// PURE : la règle tient dans le cœur, et le magasin ne fait que rendre les deux
// listes. Les deux sont supposées normalisées.
func EstAdministrateurParGroupe(groupes, admins []string) bool {
	for _, g := range groupes {
		if slices.Contains(admins, g) {
			return true
		}
	}
	return false
}

// ajouterLAdministrationParGroupe complète `IsAdmin` d'une identité
// authentifiée, selon les groupes de sa session.
//
// L'administration par groupe n'est JAMAIS écrite en base : elle est calculée
// à chaque requête, de sorte que retirer un groupe de `admins` prend effet à la
// requête suivante, sans rien à resynchroniser.
//
// Une panne de lecture est rendue, pas absorbée : traiter « on n'a pas pu
// savoir » comme « pas administrateur » cacherait une panne derrière un refus,
// et l'inverse ouvrirait l'administration à une panne.
func (a *Authenticator) ajouterLAdministrationParGroupe(ctx context.Context, user *User, groupes []string) error {
	if user.IsAdmin || len(groupes) == 0 || a.comptesAnnuaire == nil {
		return nil
	}
	admins, err := a.comptesAnnuaire.GroupesAdmin(ctx)
	if err != nil {
		return fmt.Errorf("lecture des groupes d'administration : %w", err)
	}
	user.IsAdmin = EstAdministrateurParGroupe(groupes, admins)
	return nil
}

// PrefixeGroupe distingue un groupe d'un compte local dans `grants.yaml`.
const PrefixeGroupe = "group:"

// NormaliserGroupe rend la forme sous laquelle un nom de groupe est comparé.
//
// En minuscules, parce que l'AD compare les `sAMAccountName` sans tenir compte
// de la casse : `GG-Warden-Prod` dans `grants.yaml` et `gg-warden-prod` renvoyé
// par l'annuaire sont le même groupe, et les distinguer donnerait un droit que
// personne n'exerce sans que rien ne le signale.
func NormaliserGroupe(nom string) string {
	return strings.ToLower(strings.TrimSpace(nom))
}

// normaliserLesGroupes trie, dédoublonne et écarte les noms vides.
func normaliserLesGroupes(brut []string) []string {
	vus := map[string]struct{}{}
	for _, g := range brut {
		if n := NormaliserGroupe(g); n != "" {
			vus[n] = struct{}{}
		}
	}
	out := make([]string, 0, len(vus))
	for g := range vus {
		out = append(out, g)
	}
	sort.Strings(out)
	return out
}

// BrancherLAnnuaire fait répondre l'annuaire pour tout login sans compte local
// actif.
//
// Une méthode et non un paramètre de NewAuthenticator : un déploiement sans
// annuaire est un déploiement valide, et ses appelants n'ont pas à connaître
// ce port.
func (a *Authenticator) BrancherLAnnuaire(annuaire Annuaire, comptes ComptesAnnuaire, duree time.Duration) error {
	if annuaire == nil || comptes == nil {
		return errors.New("annuaire : l'annuaire et le magasin des comptes sont requis")
	}
	if duree <= 0 {
		duree = DureeSessionAnnuaire
	}
	a.annuaire = annuaire
	a.comptesAnnuaire = comptes
	a.dureeAnnuaire = duree
	return nil
}

// parLAnnuaire tente la connexion par l'annuaire.
//
// Appelée seulement quand aucun compte local actif ne porte ce login : son
// résultat est celui de la connexion, refus et panne compris (ADR-0053,
// décision 1).
func (a *Authenticator) parLAnnuaire(ctx context.Context, login string, motDePasse []byte) (Ouverture, error) {
	id, err := a.annuaire.Authentifier(ctx, login, motDePasse)
	switch {
	case errors.Is(err, ErrIdentifiantsRefuses):
		return Ouverture{}, ErrIdentifiantsRefuses
	case err != nil:
		return Ouverture{}, fmt.Errorf("%w : %w", ErrAnnuaireIndisponible, err)
	}

	user, err := a.comptesAnnuaire.UpsertUser(ctx, Identity{
		Type: UserLDAP, Subject: id.Subject, DisplayName: id.DisplayName, Email: id.Email,
	})
	if err != nil {
		return Ouverture{}, fmt.Errorf("provisionnement du compte de l'annuaire : %w", err)
	}
	if user.Disabled {
		// Désactivé CÔTÉ WARDEN, alors que l'AD l'accepte : la décision locale
		// prime, c'est elle qui permet de couper un accès sans attendre l'AD.
		return Ouverture{}, ErrIdentifiantsRefuses
	}

	groupes := normaliserLesGroupes(id.Groupes)
	if err := a.ajouterLAdministrationParGroupe(ctx, &user, groupes); err != nil {
		return Ouverture{}, err
	}
	return a.ouvrir(ctx, user, a.dureeAnnuaire, groupes)
}

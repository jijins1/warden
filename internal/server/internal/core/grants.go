package core

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrEnvironnementInterdit refuse un environnement auquel l'appelant n'a pas droit.
//
// Distincte d'`ErrNotFound` : un environnement inconnu et un environnement
// interdit ne demandent pas la même conduite. Le premier est une faute de frappe,
// le second une demande d'accès à faire.
var ErrEnvironnementInterdit = errors.New("environnement interdit")

// EnvironmentGrant est un droit d'accès à un environnement (§10.2).
//
// Le modèle du §10.2 est `(utilisateur|groupe) → environnement → scope d'hôtes
// → mode`. Ce type porte l'UTILISATEUR ; le GROUPE est `GroupGrant`, venu avec
// l'annuaire (ADR-0053). Le scope d'hôtes attend le réseau qui l'appliquerait —
// sans lui, il ne serait que déclaratif.
type EnvironmentGrant struct {
	ID          int64
	UserID      int64
	Subject     string
	Environment string

	GrantedAt time.Time

	// GrantedBy est zéro pour les droits repris de l'historique par la migration
	// 00011 : personne ne les a accordés, et désigner quelqu'un serait faux.
	GrantedBy        int64
	GrantedBySubject string

	// RevokedAt non nul : le droit a existé et n'existe plus. Il reste lisible,
	// parce que « qui avait accès à prod le 3 mars » est une question d'audit.
	RevokedAt time.Time
}

// Revoked dit si le droit est éteint.
func (g EnvironmentGrant) Revoked() bool { return !g.RevokedAt.IsZero() }

// EnvironmentGrants lit et écrit les droits par environnement.
type EnvironmentGrants interface {
	// PeutAtteindre dit si l'utilisateur a un droit VIVANT sur l'environnement.
	//
	// Un droit vivant est NOMINATIF, ou porté par un GROUPE de l'annuaire
	// (ADR-0053) : il suffit qu'UNE session vivante du compte — non révoquée,
	// non expirée — porte un groupe qui a un droit vivant sur l'environnement.
	// N'importe laquelle, et pas celle d'une requête : à la livraison d'un ordre
	// il n'y a pas de requête. C'est ce qui borne un retrait de groupe dans l'AD
	// à la durée de la session, et ce qui retire la clé à un run dont l'auteur
	// s'est déconnecté.
	//
	// Un booléen et non un descripteur : l'appelant n'a qu'une décision à
	// prendre, et lui rendre la ligne l'inviterait à interpréter `revoked_at`
	// lui-même — c'est-à-dire à dupliquer la règle.
	PeutAtteindre(ctx context.Context, userID int64, environment string) (bool, error)

	// EnvironnementsAtteignables rend, triés, les environnements sur lesquels
	// l'utilisateur a un droit VIVANT. Même règle que PeutAtteindre, posée sur
	// tous les environnements à la fois, groupes de session compris : c'est ce
	// qui permet de lister les runs
	// de chacun sans une question par environnement.
	EnvironnementsAtteignables(ctx context.Context, userID int64) ([]string, error)

	// ListGrants rend les droits, tous ou ceux d'un compte. Les RÉVOQUÉS sont
	// inclus : c'est une vue d'audit, pas une liste de ce qui est permis.
	ListGrants(ctx context.Context, subject string) ([]EnvironmentGrant, error)

	// ListGroupGrants rend les droits des groupes de l'annuaire (ADR-0053),
	// révoqués compris, pour la même raison.
	ListGroupGrants(ctx context.Context) ([]GroupGrant, error)

	// Grant accorde un droit. Idempotent : réaccorder un droit vivant le rend
	// tel quel plutôt que d'échouer, parce qu'un opérateur qui rejoue une
	// commande ne doit pas avoir à se demander si elle a pris.
	Grant(ctx context.Context, subject, environment string, parQui int64) (EnvironmentGrant, error)

	// Revoke éteint un droit. Idempotent pour la même raison.
	Revoke(ctx context.Context, subject, environment string) error

	// AppliquerLePlan exécute une réconciliation déclarative, TOUT OU RIEN.
	//
	// Une seule transaction : un plan à moitié appliqué laisserait un état que ni
	// le fichier ni la base ne décrivent — des révocations faites, des
	// attributions non — c'est-à-dire l'incohérence que le versionnement existe
	// pour supprimer.
	AppliquerLePlan(ctx context.Context, plan GrantPlan, parQui int64) error
}

// VerifierLAcces refuse un environnement auquel l'utilisateur n'a pas droit.
//
// Fonction et non méthode d'un service : les deux appelants sont à deux étages
// différents — l'API à la création du run, le composeur d'ordres à la livraison
// de la clé — et leur donner un objet commun n'ajouterait qu'un assemblage.
//
// LE CONTRÔLE EST FAIT DEUX FOIS, délibérément :
//
//   - à la CRÉATION, pour refuser tôt et avec un message utile ;
//   - à la LIVRAISON, parce que c'est là que la clé sort. C'est ce second appel
//     qui donne un sens à la révocation : sans lui, retirer un droit ne ferait
//     rien aux runs déjà enregistrés, et le §10.3 exige que le changement de
//     droits prenne effet.
//
// Un environnement VIDE est AUTORISÉ, et ce n'est pas un trou : ce contrôle
// garde la clé d'accès, et un run sans environnement n'en reçoit aucune —
// `ValidateEnvironment` refuse la chaîne vide bien avant le magasin de clés. Sans
// credential, le pod ne peut joindre aucune machine. Refuser ici casserait au
// contraire les playbooks en `connection: local`, qui n'ont jamais eu besoin
// d'environnement et sont le cas de tous les runs du produit jusqu'ici.
func VerifierLAcces(ctx context.Context, droits EnvironmentGrants, userID int64, environment string) error {
	if environment == "" {
		return nil
	}
	if droits == nil {
		// Aucun magasin de droits monté. Le REFUS est le défaut : un contrôle
		// d'autorisation qui s'efface quand sa dépendance manque n'est pas un
		// contrôle, et un déploiement mal assemblé doit se voir tout de suite.
		return fmt.Errorf("%w : les droits par environnement ne sont pas disponibles",
			ErrEnvironnementInterdit)
	}

	ok, err := droits.PeutAtteindre(ctx, userID, environment)
	if err != nil {
		// Une panne de lecture ne DONNE PAS l'accès. C'est la différence entre
		// un contrôle et une formalité.
		return fmt.Errorf("vérification des droits sur %q : %w", environment, err)
	}
	if !ok {
		return fmt.Errorf("%w : %q", ErrEnvironnementInterdit, environment)
	}
	return nil
}

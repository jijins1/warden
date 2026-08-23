package core

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrIdentifiantsRefuses est la SEULE erreur qu'une connexion ratée produit.
//
// Un compte inconnu, un mauvais mot de passe et un compte désactivé rendent la
// même valeur. Les distinguer confirmerait à qui essaie qu'un compte existe, ce
// qui est exactement ce qu'on cherche à ne pas dire — et le message côté client
// ne pourrait de toute façon rien en faire d'utile.
//
// La journalisation, elle, distingue : c'est côté serveur que la différence a
// une valeur, pour l'exploitation.
var ErrIdentifiantsRefuses = errors.New("identifiants refusés")

// DelaiPlancher est la durée minimale d'une tentative de connexion.
//
// Deux effets, tous deux voulus. Il UNIFORMISE le temps de réponse, de sorte
// qu'un compte inconnu et un mauvais mot de passe ne se distinguent pas au
// chronomètre. Et combiné au plafond de tentatives simultanées ci-dessous, il
// borne le débit de martelage : c'est le seul frein réel dont dispose un
// endpoint que n'importe qui peut appeler sans être authentifié.
const DelaiPlancher = 250 * time.Millisecond

// TentativesSimultanees plafonne les dérivations argon2 en vol.
//
// Sans lui, l'endpoint de connexion est un déni de service à lui seul : chaque
// tentative alloue 19 Mio, et rien n'empêche d'en lancer mille à la fois. Le
// plafond transforme le coût de calcul — qui protège les mots de passe — en une
// file d'attente plutôt qu'en une consommation mémoire illimitée.
//
// Quatre : de quoi ne pas sérialiser une équipe qui arrive le matin, et 76 Mio
// au pire, ce qu'un pod encaisse.
const TentativesSimultanees = 4

// Authenticator vérifie un mot de passe et ouvre une session.
type Authenticator struct {
	comptes  CredentialStore
	sessions SessionStore

	duree    time.Duration
	plancher time.Duration

	// places borne les dérivations simultanées. Un canal tamponné plutôt qu'un
	// sémaphore d'une bibliothèque : il s'annule avec le contexte, ce qu'un
	// verrou ne sait pas faire.
	places chan struct{}

	maintenant func() time.Time
}

// NewAuthenticator construit le service et échoue à froid.
func NewAuthenticator(comptes CredentialStore, sessions SessionStore, duree time.Duration) (*Authenticator, error) {
	if comptes == nil || sessions == nil {
		return nil, errors.New("authentification : magasin des comptes et magasin des sessions sont requis")
	}
	if duree <= 0 {
		duree = DureeSession
	}
	return &Authenticator{
		comptes: comptes, sessions: sessions,
		duree:      duree,
		plancher:   DelaiPlancher,
		places:     make(chan struct{}, TentativesSimultanees),
		maintenant: time.Now,
	}, nil
}

// Ouverture est ce qu'une connexion réussie rend.
type Ouverture struct {
	Token     SessionToken
	User      User
	ExpiresAt time.Time
}

// Login vérifie un mot de passe et ouvre une session.
//
// Le déroulé est contraint par ce qu'on refuse de divulguer :
//
//  1. une place est prise dans la file — le coût de calcul ne doit pas devenir
//     un levier de saturation ;
//  2. la vérification a lieu MÊME si le compte n'existe pas, contre une
//     empreinte leurre. Sans cela, un compte inconnu répond en une microseconde
//     et un compte existant en cinquante millisecondes, ce qui suffit à énumérer
//     les comptes du produit ;
//  3. la réponse n'arrive pas avant le délai plancher, quelle qu'en soit
//     l'issue ;
//  4. toutes les issues négatives rendent la même erreur.
func (a *Authenticator) Login(ctx context.Context, subject string, motDePasse []byte) (Ouverture, error) {
	debut := a.maintenant()
	defer a.attendreLePlancher(ctx, debut)

	select {
	case a.places <- struct{}{}:
		defer func() { <-a.places }()
	case <-ctx.Done():
		return Ouverture{}, ctx.Err()
	}

	user, empreinte, err := a.comptes.EmpreinteDuCompte(ctx, subject)
	switch {
	case errors.Is(err, ErrNotFound):
		// Le compte n'existe pas — et on dérive quand même. Voir l'étape 2.
		empreinte = EmpreinteLeurre
	case err != nil:
		return Ouverture{}, fmt.Errorf("lecture du compte : %w", err)
	}

	ok, err := VerifierMotDePasse(empreinte, motDePasse)
	if err != nil {
		// Empreinte illisible : un défaut de DONNÉES, pas un mauvais mot de
		// passe. Le distinguer ici est ce qui évite qu'une corruption se présente
		// des mois durant comme un utilisateur qui se trompe de touche.
		return Ouverture{}, fmt.Errorf("compte %q : %w", subject, err)
	}
	if !ok || user.ID == 0 || user.Disabled {
		return Ouverture{}, ErrIdentifiantsRefuses
	}

	token, err := NewSessionToken()
	if err != nil {
		return Ouverture{}, err
	}
	expire := a.maintenant().Add(a.duree)

	if _, err := a.sessions.CreateSession(ctx, user.ID, token.Hash(), expire); err != nil {
		return Ouverture{}, fmt.Errorf("ouverture de session : %w", err)
	}
	return Ouverture{Token: token, User: user, ExpiresAt: expire}, nil
}

// attendreLePlancher tient la réponse jusqu'au délai minimal.
//
// L'annulation du contexte l'interrompt : faire patienter un appelant déjà parti
// occuperait une place de la file pour rien, ce qui retournerait le frein contre
// le produit.
func (a *Authenticator) attendreLePlancher(ctx context.Context, debut time.Time) {
	reste := a.plancher - a.maintenant().Sub(debut)
	if reste <= 0 {
		return
	}
	t := time.NewTimer(reste)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

// Logout révoque une session. Idempotent.
func (a *Authenticator) Logout(ctx context.Context, id int64) error {
	return a.sessions.RevokeSession(ctx, id)
}

// ChangerMotDePasse pose un nouveau mot de passe et coupe toutes les sessions.
//
// L'ANCIEN est exigé même si l'appelant est déjà authentifié : un poste laissé
// déverrouillé suffirait sinon à s'approprier le compte de façon durable, là où
// un jeton volé expire et se révoque. C'est la même raison qui fait redemander
// le mot de passe avant un changement d'email partout ailleurs.
func (a *Authenticator) ChangerMotDePasse(ctx context.Context, user User, ancien, nouveau []byte) error {
	_, empreinte, err := a.comptes.EmpreinteDuCompte(ctx, user.Subject)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			// Un compte sans mot de passe local : OIDC, LDAP, ou un compte créé
			// sans identifiants. Il n'y a rien à changer ici.
			return fmt.Errorf("%w : ce compte n'a pas de mot de passe local", ErrIdentifiantsRefuses)
		}
		return fmt.Errorf("lecture du compte : %w", err)
	}

	ok, err := VerifierMotDePasse(empreinte, ancien)
	if err != nil {
		return err
	}
	if !ok {
		return ErrIdentifiantsRefuses
	}

	return a.PoserMotDePasse(ctx, user.ID, nouveau, user.ID)
}

// PoserMotDePasse écrit un mot de passe SANS vérifier l'ancien.
//
// Réservée aux chemins qui ont établi l'autorisation autrement : l'amorçage du
// premier compte, et un administrateur qui remet un compte en service. Le nom ne
// dit pas « réinitialiser » exprès — il ne doit pas se lire comme une commodité.
func (a *Authenticator) PoserMotDePasse(ctx context.Context, userID int64, motDePasse []byte, parQui int64) error {
	empreinte, err := Empreinte(motDePasse)
	if err != nil {
		return err
	}
	return a.comptes.PoserMotDePasse(ctx, userID, empreinte, parQui)
}

// Authentifier résout un jeton présenté en identité.
//
// C'est le chemin de CHAQUE requête authentifiée, d'où la paresse du
// rafraîchissement : `last_seen_at` n'est réécrit qu'au-delà d'une heure, sans
// quoi le produit ferait une écriture par lecture.
func (a *Authenticator) Authentifier(ctx context.Context, token SessionToken) (User, Session, error) {
	session, user, err := a.sessions.SessionByToken(ctx, token.Hash())
	if err != nil {
		return User{}, Session{}, err
	}
	if user.Disabled {
		// Une session survivante sur un compte désactivé : la désactivation
		// révoque les sessions, mais la course existe et le compte prime.
		return User{}, Session{}, ErrNotFound
	}

	// L'échec n'empêche RIEN : `last_seen_at` est du confort d'exploitation, et
	// refuser une requête authentifiée parce qu'une statistique n'a pas pu
	// s'écrire serait une panne fabriquée.
	_ = a.sessions.TouchSession(ctx, session.ID, FraicheurSession)

	return user, session, nil
}

// FraicheurSession est l'âge au-delà duquel `last_seen_at` est réécrit.
const FraicheurSession = time.Hour

package core

import (
	"context"
	"errors"
	"fmt"
	"sort"
)

// RawEnvironmentOptions est ce qu'une entrée du document déclare TELLE QUELLE,
// avant normalisation.
//
// `AllowRun` est un POINTEUR et non un `bool` : la clé absente doit se lire
// « autorisé » (valeur par défaut), pas la valeur zéro d'un bool qui
// bloquerait silencieusement tous les runs d'un environnement au premier
// oubli d'écrire `allowRun: true`. C'est la même prudence que
// `EnvironmentGrant.RevokedAt`, où l'absence et le zéro ne doivent jamais se
// confondre.
type RawEnvironmentOptions struct {
	Protected bool  `json:"protected,omitempty" yaml:"protected,omitempty"`
	AllowRun  *bool `json:"allowRun,omitempty" yaml:"allowRun,omitempty"`
}

// ProtectedDocument est ce qu'un fichier de configuration versionné déclare.
//
// Une CARTE, comme GrantDocument : chaque environnement porte ses propres
// options, et c'est ce qui permet à `allowRun: false` de geler `prod` sans
// toucher à `staging`. Un environnement ABSENT du document n'est concerné par
// AUCUNE des deux options — ni protégé visuellement, ni gelé — même principe
// que la portée de PlanifierLesDroits.
type ProtectedDocument struct {
	Environments map[string]RawEnvironmentOptions `json:"environments" yaml:"environments"`
}

// EnvironmentOptions est la forme NORMALISÉE d'une entrée, après validation :
// `AllowRun` n'est plus un pointeur, la question qu'il pose a déjà sa réponse.
type EnvironmentOptions struct {
	Environment string
	Protected   bool
	AllowRun    bool
}

// ValiderLesProteges valide et normalise le document : chaque nom doit avoir
// la forme attendue par ValidateEnvironment, et `AllowRun` absent devient
// `true`. Le résultat est trié par environnement.
//
// PURE : ni fichier ni base, comme PlanifierLesDroits. C'est ce qui rend la
// validation vérifiable sans infrastructure.
//
// À la différence de PlanifierLesDroits, un document VIDE n'est PAS refusé :
// contrairement aux droits, oublier de déclarer un environnement n'ouvre
// aucun accès — au pire ça retire un badge, au mieux ça laisse `allowRun` à
// son défaut sûr (autorisé). « Ne rien vouloir » n'a donc pas besoin d'être
// distingué d'un fichier tronqué ici.
func ValiderLesProteges(doc ProtectedDocument) ([]EnvironmentOptions, error) {
	envs := make([]string, 0, len(doc.Environments))
	for env := range doc.Environments {
		if err := ValidateEnvironment(env); err != nil {
			return nil, fmt.Errorf("environnement %q : %w", env, err)
		}
		envs = append(envs, env)
	}
	sort.Strings(envs)

	out := make([]EnvironmentOptions, 0, len(envs))
	for _, env := range envs {
		brut := doc.Environments[env]
		out = append(out, EnvironmentOptions{
			Environment: env,
			Protected:   brut.Protected,
			AllowRun:    brut.AllowRun == nil || *brut.AllowRun,
		})
	}
	return out, nil
}

// ErrRunBloque refuse le lancement d'un run sur un environnement GELÉ.
var ErrRunBloque = errors.New("lancement de run bloqué sur cet environnement")

// ProtectedEnvironments lit et remplace les options par environnement, et
// répond à la question posée à chaque lancement de run.
//
// CE N'EST PAS UN CONTRÔLE D'ACCÈS À UN CREDENTIAL — voir EnvironmentGrants
// pour ça (ADR-0033). `Protected` est un avertissement visuel ; `AllowRun` est
// un GEL opérationnel, volontairement indépendant des droits individuels : un
// exploitant peut geler `prod` sans retoucher qui y a droit, puis dégeler
// sans avoir à réaccorder quoi que ce soit.
type ProtectedEnvironments interface {
	// ListOptions rend les options actuellement en base, triées par
	// environnement. Une liste vide est un état valide : « rien n'est déclaré »
	// n'est pas une panne.
	ListOptions(ctx context.Context) ([]EnvironmentOptions, error)

	// ReplaceOptions remplace la liste ENTIÈRE par celle donnée, en une
	// transaction.
	//
	// Un remplacement complet et non un diff sujet par sujet comme les droits
	// (ADR-0033) : il n'y a ici ni utilisateur ni date à préserver par ligne,
	// juste des options par environnement. Le document déclaratif fait autorité
	// dans son ensemble.
	ReplaceOptions(ctx context.Context, options []EnvironmentOptions) error

	// RunAutorise dit si le lancement d'un run est actuellement permis sur cet
	// environnement. Un environnement SANS ligne rend `true` : l'absence de
	// déclaration ne gèle rien, même raison que l'absence d'un environnement du
	// document des droits ne révoque rien.
	RunAutorise(ctx context.Context, environment string) (bool, error)
}

// VerifierRunAutorise refuse un run sur un environnement GELÉ par le document
// des environnements protégés (ADR-0044, `allowRun: false`).
//
// DISTINCT de VerifierLAcces (ADR-0033) : ceci ne garde aucun credential, ce
// n'est qu'un gel opérationnel. Deux conséquences suivent directement de
// cette différence, et c'est tout l'objet de cette fonction par rapport à sa
// cousine :
//
//   - un environnement VIDE est autorisé, même raison que VerifierLAcces —
//     `connection: local` n'a jamais eu besoin d'environnement ;
//   - un magasin ABSENT est autorisé, et c'est l'INVERSE de VerifierLAcces.
//     Le gel est une option qu'un exploitant active en déployant un fichier ;
//     un déploiement qui ne la monte pas doit continuer à lancer des runs
//     normalement, alors qu'un magasin de DROITS absent doit tout refuser.
func VerifierRunAutorise(ctx context.Context, options ProtectedEnvironments, environment string) error {
	if environment == "" || options == nil {
		return nil
	}

	ok, err := options.RunAutorise(ctx, environment)
	if err != nil {
		return fmt.Errorf("vérification du gel de %q : %w", environment, err)
	}
	if !ok {
		return fmt.Errorf("%w : %q", ErrRunBloque, environment)
	}
	return nil
}

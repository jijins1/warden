package core

import (
	"context"
	"fmt"
	"strings"
)

// PodSpec est ce qu'il faut savoir pour lancer un pod runner.
//
// Le cœur ne décrit PAS un objet Kubernetes : il dit ce que le pod doit être, et
// l'adaptateur traduit. C'est la condition posée par le `doc.go` de
// l'orchestrateur — l'ADR-0010 a décidé une stratégie de test sans cluster
// jetable, et elle n'est réalisable que si client-go reste derrière une couture.
type PodSpec struct {
	// Name est le nom du pod, déjà enregistré en base quand il est lancé.
	Name string

	// Image est déclarée par le projet (ADR-0014), qui épingle du même coup
	// ansible-core, l'interpréteur Python et le système.
	Image string

	// Token est le jeton d'amorçage. L'adaptateur le monte par un Secret dédié,
	// jamais par une variable d'environnement : l'objet `Pod` est celui que le
	// produit liste, observe et journalise le plus, et un secret dans le champ le
	// plus consulté du système est un secret qui finira dans un log (ADR-0023).
	Token PodToken

	// Pool identifie l'utilisateur, le dépôt et l'environnement. Il sert aux
	// étiquettes du pod, donc à la NetworkPolicy qui borne son egress (ADR-0007).
	Pool PodPool
}

// PodLauncher crée et supprime les pods runner.
//
// LE port dont le `doc.go` de l'orchestrateur dit que « l'existence n'est pas
// négociable ». Tout ce qui suit se teste à l'étage 1 contre une implémentation
// factice ; le jour où client-go entre dans le module, il entre ici et nulle part
// ailleurs.
type PodLauncher interface {
	// LaunchPod crée le pod. Il doit être IDEMPOTENT sur le nom : la leader
	// election par Lease ne garantit pas l'unicité, et un contrôleur qui a perdu
	// son bail sans le savoir peut agir en double.
	LaunchPod(ctx context.Context, spec PodSpec) error

	// DeletePod supprime le pod. Idempotent de même.
	DeletePod(ctx context.Context, name string) error
}

// PodRegistry enregistre l'identité d'amorçage d'un pod.
type PodRegistry interface {
	// RegisterPod enregistre le pod et rend son identifiant. Le jeton n'est pas
	// stocké : seule son empreinte l'est (ADR-0023).
	RegisterPod(ctx context.Context, n NewPod) (int64, error)
}

// RunStarter met un run en route.
//
// Un pod par run, créé à la demande. Le pré-chauffage du §8.4 n'est pas ici, et
// c'est délibéré : c'est une OPTIMISATION, pas une fonctionnalité. Sans lui le
// produit fonctionne et démarre plus lentement — l'ADR-0015 le rendait
// obligatoire sur une conclusion de performance, pas de correction.
//
// Le jour où le pool revient, il se branche en amont de cette séquence : réclamer
// d'abord, ne créer qu'en cas d'absence. `ErrNoWarmPod` décrit déjà ce cas comme
// nominal, et le champ `RunID` de l'enregistrement distingue déjà les deux
// origines.
type RunStarter struct {
	pods     PodRegistry
	launcher PodLauncher
	spaces   RunNamespacer
	image    string
}

// NewRunStarter construit le démarreur.
func NewRunStarter(pods PodRegistry, launcher PodLauncher, spaces RunNamespacer, image string) (*RunStarter, error) {
	if pods == nil || launcher == nil || spaces == nil {
		return nil, fmt.Errorf("démarrage de run : registre, lanceur et espaces de noms sont requis")
	}
	if image == "" {
		// Sans image, le pod échouerait au démarrage avec une erreur de
		// Kubernetes, loin de la cause. L'ADR-0014 la fait déclarer par le projet ;
		// en attendant cette configuration, elle vient du déploiement.
		return nil, fmt.Errorf("démarrage de run : image du runner absente")
	}
	return &RunStarter{pods: pods, launcher: launcher, spaces: spaces, image: image}, nil
}

// Start prépare et lance le pod d'un run.
//
// L'ORDRE porte la correction, et chaque étape échoue avant la suivante :
//
//  1. le jeton d'amorçage est frappé et ENREGISTRÉ avant que le pod existe.
//     L'ADR-0023 l'exige : le Secret qui le porte doit exister avant que le
//     kubelet monte le volume, sans quoi le pod attend au montage ;
//  2. la ref d'espace de noms est créée AVANT le lancement. C'est elle que le
//     runner viendra chercher, et l'ordre inverse ouvrirait une course où un pod
//     rapide fetch une ref qui n'existe pas encore — échec qui se présenterait
//     comme un dépôt vide ;
//  3. le pod est lancé en dernier, quand tout ce dont il a besoin est en place.
//
// Le pod naît ATTACHÉ à son run. Le faire naître libre pour le réclamer aussitôt
// passerait par le `ready_at IS NOT NULL` du claim, donc obligerait à attendre
// qu'il se connecte avant de lui attribuer le run pour lequel il vient d'être
// créé.
func (s *RunStarter) Start(ctx context.Context, run Run) (string, error) {
	if run.ID == 0 || run.RunRef == "" {
		return "", fmt.Errorf("démarrage : run sans identifiant")
	}
	if run.Repo == "" {
		return "", fmt.Errorf("démarrage du run %s : sans dépôt, le runner n'a rien à tirer", run.RunRef)
	}
	if run.AuditSHA == "" {
		return "", fmt.Errorf("démarrage du run %s : sans commit d'audit, il n'y a rien à exécuter", run.RunRef)
	}

	token, err := NewPodToken()
	if err != nil {
		return "", fmt.Errorf("démarrage du run %s : %w", run.RunRef, err)
	}

	// Le nom dérive du run et non d'un aléa : il rend le pod retrouvable depuis
	// l'audit sans jointure, et rend le lancement idempotent sur le nom — ce que
	// le port exige, la leader election ne garantissant pas l'unicité.
	name := "warden-run-" + strings.ToLower(run.RunRef)
	pool := PodPool{UserID: run.UserID, Repo: run.Repo, Environment: run.Environment}

	if _, err := s.pods.RegisterPod(ctx, NewPod{
		PodName: name, Pool: pool, Token: token, RunID: run.ID,
	}); err != nil {
		return "", fmt.Errorf("démarrage du run %s : %w", run.RunRef, err)
	}

	// La ref que le runner verra comme sa seule ref (ADR-0007). Le rôle `git`
	// compose lui-même le chemin réel, imbrication comprise : l'écrire à la main
	// produit un chemin qui n'est jamais annoncé, sans erreur ni trace (ADR-0026).
	if err := s.spaces.CreateRunNamespace(ctx, run.Repo, RunNamespaceFor(run.RunRef), run.AuditSHA); err != nil {
		return "", fmt.Errorf("démarrage du run %s : espace de noms : %w", run.RunRef, err)
	}

	if err := s.launcher.LaunchPod(ctx, PodSpec{
		Name: name, Image: s.image, Token: token, Pool: pool,
	}); err != nil {
		// Le pod n'existe pas, mais sa ligne et sa ref si. Les deux sont inertes —
		// la ligne n'est jamais prête donc jamais servie, la ref est un pointeur
		// vers un commit qui existe déjà. Le reaper du §8.7 les ramasse ; les
		// supprimer ici, dans le chemin d'erreur, ajouterait deux appels qui
		// peuvent échouer à leur tour.
		return "", fmt.Errorf("démarrage du run %s : lancement du pod : %w", run.RunRef, err)
	}

	return name, nil
}

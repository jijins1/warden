package keyinventory

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"time"

	"golang.org/x/crypto/ssh"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/jijins1/warden/internal/server/internal/core"
)

// CleSecret est la clé de données du Secret portant la partie privée.
//
// Le nom est celui qu'`ansible-runner` attend dans `<pdd>/env/ssh_key`, et ce
// n'est pas une coïncidence entretenue : le runner recopie ce contenu sous ce
// nom-là, et garder le même mot des deux côtés supprime une conversion.
const CleSecret = "ssh_key"

// Etiquettes posées sur les Secrets de clés.
//
// Elles ne sont pas décoratives : elles rendent les clés listables et
// supprimables sans deviner leur nom, ce qui est la première chose dont on a
// besoin le jour où l'on nettoie un namespace.
const (
	LabelComposant   = "app.kubernetes.io/name"
	LabelRole        = "app.kubernetes.io/component"
	LabelEnvironment = "warden.io/environment"

	ValeurComposant = "warden"
	ValeurRole      = "access-key"
)

// Kubernetes est l'adaptateur `key-inventory` de l'ADR-0020.
//
// Il tient la moitié SENSIBLE d'une clé — la partie privée — dans un Secret du
// namespace des pods. L'autre moitié, publique, vit dans PostgreSQL. La
// séparation est la décision : un seul magasin obligerait à choisir entre un
// journal d'audit qui contient des secrets et une configuration qu'on ne peut
// pas requêter.
//
// Ce que la v1 NE FAIT PAS, à lire sans atténuation (ADR-0020) : aucun
// certificat éphémère, aucune expiration, aucun chiffrement au repos par défaut
// — un Secret est du base64 dans etcd. Compromettre `wardend` donne toutes les
// clés d'un coup, sans borne temporelle.
type Kubernetes struct {
	client    kubernetes.Interface
	namespace string
	catalogue core.KeyCatalog
}

// New construit l'adaptateur et échoue à froid.
func New(client kubernetes.Interface, namespace string, catalogue core.KeyCatalog) (*Kubernetes, error) {
	if client == nil {
		return nil, errors.New("magasin de clés : client Kubernetes absent")
	}
	if namespace == "" {
		// Sans namespace, les appels partiraient vers `default` — hors du seul
		// endroit où le produit a des droits, et potentiellement dans un
		// namespace qui appartient à quelqu'un d'autre.
		return nil, errors.New("magasin de clés : namespace absent")
	}
	if catalogue == nil {
		// Sans catalogue, la partie publique ne serait écrite nulle part : on
		// poserait une clé privée que personne ne pourrait déployer.
		return nil, errors.New("magasin de clés : catalogue absent")
	}
	return &Kubernetes{client: client, namespace: namespace, catalogue: catalogue}, nil
}

// Issue rend la clé d'accès d'un environnement (ADR-0020).
//
// `user` et `hostScope` sont reçus et IGNORÉS, et c'est une décision locale à
// cet adaptateur, pas une lacune du port : la v1 rend la clé de l'environnement.
// Ce qu'elle coûte est déjà enregistré par l'ADR-0020 — « l'audit interne dira
// alice, les logs de l'hôte diront ansible ».
//
// L'expiration rendue est NULLE : il n'y a pas de lease qui referme la fenêtre,
// et la rotation est le seul mécanisme de révocation.
func (k *Kubernetes) Issue(ctx context.Context, _, environment string, _ []string) (core.AccessKey, error) {
	if err := core.ValidateEnvironment(environment); err != nil {
		return core.AccessKey{}, err
	}

	secret, err := k.client.CoreV1().Secrets(k.namespace).
		Get(ctx, core.SecretNameFor(environment), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return core.AccessKey{}, fmt.Errorf("environnement %s: %w", environment, core.ErrNoKey)
	}
	if err != nil {
		// Le message ne cite ni le contenu ni rien qui en dépende : cette erreur
		// remonte jusqu'à des traces.
		return core.AccessKey{}, fmt.Errorf("lecture de la clé de %s: %w", environment, err)
	}

	privee := secret.Data[CleSecret]
	if len(privee) == 0 {
		// Un Secret présent mais vide est pire qu'absent : il fait croire
		// l'environnement configuré, et le run échouerait sur une erreur SSH.
		return core.AccessKey{}, fmt.Errorf("environnement %s : le Secret %s ne porte pas de clé sous %q",
			environment, secret.Name, CleSecret)
	}
	// Expiration NULLE : voir la note de cette méthode. Le champ existe pour
	// l'adaptateur Vault, il ne ment pas ici — il dit « pas de borne », ce qui
	// est exactement l'état des choses.
	return core.NewAccessKey(privee, time.Time{}), nil
}

// Import pose une partie privée fournie par un opérateur.
func (k *Kubernetes) Import(ctx context.Context, environment string, private []byte) (core.KeyDescriptor, error) {
	if err := core.ValidateEnvironment(environment); err != nil {
		return core.KeyDescriptor{}, err
	}

	publique, empreinte, err := publiqueDe(private)
	if err != nil {
		return core.KeyDescriptor{}, err
	}
	// Le commentaire de la clé importée est REMPLACÉ par le nôtre : celui de
	// l'opérateur nomme souvent son poste, ce qui n'apprend rien à
	// l'administrateur qui ouvrira l'`authorized_keys` d'une machine.
	return k.poser(ctx, environment, private,
		avecCommentaire(publique, environment), empreinte, core.KeyImported)
}

// Generate frappe une paire ed25519.
//
// ed25519 et non RSA : clé courte, signature rapide, et acceptée par tout
// OpenSSH depuis 6.5 (2014). Le choix ne se discute que si une cible refuse
// l'algorithme, ce qui se verrait à l'enrôlement et pas en production.
//
// La partie privée NE QUITTE JAMAIS le cluster : elle est frappée ici, écrite
// dans le Secret, et rendue à personne. C'est ce qui distingue ce chemin de
// l'import.
func (k *Kubernetes) Generate(ctx context.Context, environment string) (core.KeyDescriptor, error) {
	if err := core.ValidateEnvironment(environment); err != nil {
		return core.KeyDescriptor{}, err
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return core.KeyDescriptor{}, fmt.Errorf("frappe de la clé de %s: %w", environment, err)
	}

	bloc, err := ssh.MarshalPrivateKey(priv, "warden "+environment)
	if err != nil {
		return core.KeyDescriptor{}, fmt.Errorf("encodage de la clé de %s: %w", environment, err)
	}

	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return core.KeyDescriptor{}, fmt.Errorf("partie publique de %s: %w", environment, err)
	}

	return k.poser(ctx, environment,
		pem.EncodeToMemory(bloc),
		avecCommentaire(ligneAutorisee(sshPub), environment),
		ssh.FingerprintSHA256(sshPub),
		core.KeyGenerated)
}

// Describe rend ce qu'on peut dire d'une clé sans la révéler.
//
// La lecture passe par le CATALOGUE et non par le Secret : le descripteur ne
// contient rien de sensible, et aller le chercher dans le magasin de secrets
// ferait passer une consultation d'administration par le chemin qui détient les
// clés privées.
func (k *Kubernetes) Describe(ctx context.Context, environment string) (core.KeyDescriptor, error) {
	return k.catalogue.KeyByEnvironment(ctx, environment)
}

// poser écrit le Secret PUIS le catalogue.
//
// L'ORDRE porte la correction, et c'est le même raisonnement que le jeton
// d'amorçage de l'ADR-0023 : le secret doit exister avant que quoi que ce soit
// le référence. Un catalogue qui pointerait un Secret absent décrirait un
// environnement configuré dont aucun run ne peut se servir, et le symptôme
// serait une erreur SSH au premier run.
//
// L'inverse — Secret écrit, catalogue non — laisse une clé privée orpheline :
// inerte, invisible, et remplacée au prochain essai.
func (k *Kubernetes) poser(ctx context.Context, environment string,
	privee []byte, publique, empreinte string, origine core.KeyOrigin,
) (core.KeyDescriptor, error) {
	nom := core.SecretNameFor(environment)

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nom,
			Namespace: k.namespace,
			Labels: map[string]string{
				LabelComposant:   ValeurComposant,
				LabelRole:        ValeurRole,
				LabelEnvironment: environment,
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{CleSecret: privee},
	}

	api := k.client.CoreV1().Secrets(k.namespace)
	_, err := api.Create(ctx, secret, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		// Remplacer EST la rotation, et l'ADR-0020 note que c'est le seul
		// mécanisme de révocation disponible. La refuser obligerait à supprimer
		// d'abord, donc à passer par un état où l'environnement n'a plus de clé.
		_, err = api.Update(ctx, secret, metav1.UpdateOptions{})
	}
	if err != nil {
		return core.KeyDescriptor{}, fmt.Errorf("écriture de la clé de %s: %w", environment, err)
	}

	d := core.KeyDescriptor{
		Environment: environment,
		PublicKey:   publique,
		Fingerprint: empreinte,
		SecretName:  nom,
		Origin:      origine,
	}
	if err := k.catalogue.SaveKey(ctx, d); err != nil {
		return core.KeyDescriptor{}, err
	}
	return d, nil
}

package orchestrator

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/jijins1/warden/internal/server/internal/core"
)

// La consigne : un magasin de valeurs qui ne font que PASSER.
//
// Un seul usage aujourd'hui, et il justifie à lui seul le mécanisme : le mot de
// passe d'un enrôlement. Il est saisi par un humain devant son terminal, remis
// au serveur par `POST /admin/enrollments`, et lu quelques secondes plus tard à
// la LIVRAISON de l'ordre — moment auquel il descend vers le pod par le canal de
// dispatch (ADR-0018).
//
// Entre les deux, il faut bien qu'il soit quelque part. Ce quelque part ne peut
// pas être PostgreSQL : l'ADR-0018 pose qu'aucun credential n'y est persisté, et
// une insertion y laisserait une trace dans le WAL, donc dans les sauvegardes et
// les réplicas — pour une valeur dont la durée de vie utile est de l'ordre de la
// seconde.
//
// C'est donc un Secret Kubernetes, comme pour les clés d'accès (ADR-0020), avec
// une différence qui change tout : celui-ci est DÉTRUIT à la lecture.

// prefixeConsigne nomme les Secrets de passage.
//
// Distinct de celui des clés d'accès et de celui des jetons de pod : un nom qui
// dit ce que la chose est rend un `kubectl get secrets` lisible, et rend
// possible de tout nettoyer sans deviner.
const prefixeConsigne = "warden-transit-"

// CleConsigne est la clé de données du Secret.
const CleConsigne = "value"

// Stash range une valeur pour un seul usage.
//
// Écrase ce qui existait : reprendre un enrôlement interrompu doit remplacer le
// mot de passe précédent, pas échouer sur sa présence.
func (k *Kubernetes) Stash(ctx context.Context, cle string, valeur []byte) error {
	if cle == "" {
		return errors.New("consigne : clé absente")
	}
	if len(valeur) == 0 {
		return errors.New("consigne : valeur vide")
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      prefixeConsigne + cle,
			Namespace: k.cfg.Namespace,
			Labels: map[string]string{
				LabelComposant:                "warden",
				"app.kubernetes.io/component": "transit",
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{CleConsigne: valeur},
	}

	api := k.client.CoreV1().Secrets(k.cfg.Namespace)
	_, err := api.Create(ctx, secret, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		_, err = api.Update(ctx, secret, metav1.UpdateOptions{})
	}
	if err != nil {
		// Le message ne cite jamais la valeur.
		return fmt.Errorf("consigne %s : %w", cle, err)
	}
	return nil
}

// Take lit la valeur ET la détruit.
//
// La destruction fait partie de l'opération, pas d'un ménage ultérieur : une
// valeur qui ne sert qu'une fois et qu'on peut relire est une valeur qui traîne.
// L'échec de la suppression n'annule PAS la lecture — l'ordre est déjà composé
// à ce moment-là, et refuser de livrer un enrôlement parce que Kubernetes n'a
// pas confirmé une suppression coûterait plus que ça ne protège. Il est
// journalisé pour que le reste soit nettoyable.
func (k *Kubernetes) Take(ctx context.Context, cle string) ([]byte, error) {
	if cle == "" {
		return nil, errors.New("consigne : clé absente")
	}
	nom := prefixeConsigne + cle
	api := k.client.CoreV1().Secrets(k.cfg.Namespace)

	secret, err := api.Get(ctx, nom, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("consigne %s : %w", cle, core.ErrNoStash)
	}
	if err != nil {
		return nil, fmt.Errorf("consigne %s : %w", cle, err)
	}

	valeur := secret.Data[CleConsigne]
	if len(valeur) == 0 {
		return nil, fmt.Errorf("consigne %s : %w", cle, core.ErrNoStash)
	}

	// Suppression immédiate. `Delete` est idempotent côté API pour ce qui nous
	// intéresse : deux livraisons concurrentes du même ordre — que rien
	// n'empêche formellement — liraient la même valeur, et une seule
	// supprimerait.
	if err := api.Delete(ctx, nom, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return valeur, fmt.Errorf("consigne %s : lue mais non détruite : %w", cle, err)
	}
	return valeur, nil
}

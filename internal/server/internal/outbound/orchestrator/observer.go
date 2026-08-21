package orchestrator

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/jijins1/warden/internal/server/internal/core"
)

// NomConteneur est le conteneur qui porte le runner.
//
// Nommé plutôt que pris au rang zéro : le jour où un sidecar apparaît, lire
// `ContainerStatuses[0]` rapporterait l'état de l'autre conteneur — et une cause
// de terminaison fausse est pire qu'une cause absente.
const NomConteneur = "runner"

// ObserverPods rend les faits sur les pods runner du namespace, par nom.
//
// SÉLECTEUR DE LABELS, toujours : le `doc.go` de ce paquet en fait une règle —
// « jamais un list non filtré, même dans un namespace dédié ». Ici la raison est
// concrète : le réconciliateur SUPPRIME ce qu'il ramasse, et une liste non
// filtrée lui ferait ramasser des objets qui ne lui appartiennent pas.
//
// Un LIST et non un informer, à ce stade. Le §8.7 parle d'un watch, et ce sera
// la bonne forme quand le volume l'exigera ; un list périodique donne exactement
// la même réponse, ne peut pas rater d'événement pendant une reconnexion, et
// n'introduit pas de cache dont la fraîcheur devient une question. Le passage à
// l'informer ne changera pas ce que le cœur voit.
func (k *Kubernetes) ObserverPods(ctx context.Context) (map[string]core.PodFacts, error) {
	liste, err := k.client.CoreV1().Pods(k.cfg.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: LabelComposant + "=" + ValeurComposant,
	})
	if err != nil {
		return nil, fmt.Errorf("observation des pods runner : %w", err)
	}

	faits := make(map[string]core.PodFacts, len(liste.Items))
	for i := range liste.Items {
		f := FaitsDuPod(&liste.Items[i])
		faits[f.Name] = f
	}
	return faits, nil
}

// FaitsDuPod extrait d'un pod ce que le cœur a besoin de savoir.
//
// Elle extrait des FAITS et ne conclut rien : la traduction en cause de
// terminaison est du domaine (`core.CauseDeTerminaison`). La séparation n'est pas
// décorative — elle est ce qui rend la moitié qui compte testable sans cluster,
// alors que cette moitié-ci ne peut être vérifiée qu'en observant de vrais pods.
//
// Exportée pour cette raison : elle est le point où une hypothèse sur Kubernetes
// peut être fausse, donc le point qu'un test doit pouvoir viser directement.
func FaitsDuPod(pod *corev1.Pod) core.PodFacts {
	f := core.PodFacts{
		Name: pod.Name,
		// `status.reason` porte ce qui est arrivé au POD : « Evicted »,
		// « NodeLost », « DeadlineExceeded ». Il est vide dans le cas courant.
		PodReason: pod.Status.Reason,
	}

	// La PHASE est ce qui dit si le pod tourne encore, et c'est le seul jugement
	// porté ici. `Succeeded` y compte comme terminé au même titre que `Failed` :
	// un processus mort proprement sans avoir rapporté n'a pas pour autant réussi
	// son playbook, et c'est au cœur de le dire.
	switch pod.Status.Phase {
	case corev1.PodSucceeded, corev1.PodFailed:
		f.Termine = true
	}

	cs := statutDuConteneur(pod)
	if cs == nil {
		return f
	}

	if t := cs.State.Terminated; t != nil {
		f.ContainerReason = t.Reason
		f.ExitCode = t.ExitCode

		// Un conteneur terminé alors que la phase ne l'est pas encore : le pod est
		// en cours de mise à jour côté API. Le fait est le fait — le processus est
		// mort, et attendre la phase ne ferait que retarder l'audit.
		f.Termine = true
		return f
	}

	// `lastState` en repli, mais SEULEMENT si la phase dit déjà le pod terminé.
	// Quand le kubelet a nettoyé l'état courant, c'est la dernière trace de
	// l'OOM, et la perdre rendrait « unknown » sur le cas que le §6.4 nomme
	// explicitement. Le conditionner à la phase évite l'inverse : conclure à la
	// mort d'un pod bien vivant sur une terminaison ancienne.
	if f.Termine {
		if t := cs.LastTerminationState.Terminated; t != nil {
			f.ContainerReason = t.Reason
			f.ExitCode = t.ExitCode
		}
	}
	return f
}

// statutDuConteneur trouve le statut du conteneur runner, ou nil.
func statutDuConteneur(pod *corev1.Pod) *corev1.ContainerStatus {
	for i := range pod.Status.ContainerStatuses {
		if pod.Status.ContainerStatuses[i].Name == NomConteneur {
			return &pod.Status.ContainerStatuses[i]
		}
	}
	return nil
}

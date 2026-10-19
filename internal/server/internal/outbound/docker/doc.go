// Package docker lance les runners par l'API Docker, sans cluster (ADR-0047).
//
// Il implémente les deux mêmes ports que `outbound/orchestrator` —
// core.PodLauncher et core.PodsObserves — et le cœur n'en sait rien : un
// « pod » est ici un conteneur, du même nom.
//
// # Ce qui est transposé de Kubernetes
//
//   - le durcissement : racine en lecture seule, toutes les capacités
//     retirées, `no-new-privileges`, utilisateur 65534, seccomp par défaut,
//     `tmpfs` pour l'espace de travail et `/tmp` ;
//   - le confinement par labels (ADR-0010) : toute liste filtre sur eux, et
//     aucun conteneur sans eux n'est inspecté comme nôtre ni supprimé ;
//   - l'idempotence de LaunchPod sur le nom.
//
// # Ce qui change
//
// Le jeton d'amorçage passe par stdin : le serveur s'attache au conteneur
// avant de le démarrer, écrit le jeton, ferme. Il n'apparaît dans aucun objet
// Docker — `docker inspect` affiche `Env`, et c'est la raison même pour
// laquelle l'ADR-0023 a écarté la variable d'environnement.
//
// Docker n'a pas d'`activeDeadlineSeconds`. ObserverPods tue les conteneurs
// qui dépassent la borne et les rapporte en `DeadlineExceeded`. Si plus aucun
// contrôleur ne tourne, plus rien ne borne un run : c'est la régression que
// l'ADR-0047 assume par rapport à la décision actée n°13.
//
// `Evicted` et `NodeLost` n'existent pas : un hôte Docker perdu emporte le
// serveur avec lui ou le prive de toute observation.
package docker

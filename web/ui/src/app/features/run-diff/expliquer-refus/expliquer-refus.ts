import { ApiError } from '../../../core/models/api-error';

/**
 * Ce qu'on dit quand le code d'un run ne se lit pas : son diff, son arbre ou
 * l'un de ses fichiers.
 *
 * Le motif vient de l'en-tête `X-Warden-Reason`, jamais d'une analyse du corps :
 * le serveur le pose exactement pour ça, et lire la phrase casserait à la
 * première reformulation.
 */
export function expliquer(err: Partial<ApiError>): string {
  switch (err.reason) {
    case 'artifact_pruned':
      // ADR-0013 : le run a existé, c'est son arbre qui n'est plus là.
      return 'The code for this run was pruned. The run and its events remain; its diffs can no longer be computed.';
    case 'repo_unknown':
      return 'This run predates repository tracking, so there is nothing to diff it against.';
    case 'object_missing':
      return 'The code for this run is no longer in the repository.';
    case 'path_missing':
      return 'This file is not in the code of this run.';
    case 'file_too_large':
      return 'This file is too large to be shown here.';
    case 'no_previous_run':
      // Le serveur CONFOND délibérément « aucun run antérieur » et « il
      // appartient à quelqu'un d'autre » : les distinguer ferait de l'endpoint
      // un oracle. Le message les confond donc aussi.
      return 'No earlier run you can read on this repository, environment and inventory.';
    default:
      break;
  }
  if (err.status === 501) {
    // Un déploiement d'audit sans accès au rôle `git`, ou sans la politique de
    // dépôt et le résolveur de refs que vs=current demande en plus, reste
    // valide : ce n'est pas une panne, et le dire comme telle enverrait
    // chercher un incident.
    return 'Git diffs are not available in this deployment.';
  }
  if (err.status === 504) {
    return 'The git role did not answer in time. Try again.';
  }
  if (err.status === 502) {
    return 'The git role could not compute this diff.';
  }
  return err.message ?? 'the diff could not be computed';
}

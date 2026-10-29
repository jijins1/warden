/** Ce qu'un appel d'API rate, sous une forme que l'interface sait afficher. */
export interface ApiError {
  readonly status: number;
  readonly message: string;
  readonly cause: CauseErreur;

  /**
   * Le motif machine-lisible de l'en-tête `X-Warden-Reason`, quand il y en a un.
   *
   * Le serveur le pose EXPRESSÉMENT pour l'interface d'audit : le corps reste du
   * texte, et distinguer une purge d'un objet manquant en analysant une phrase
   * casserait à la première reformulation. Les valeurs connues sont
   * `artifact_pruned`, `repo_unknown`, `object_missing` et `no_previous_run`,
   * plus `path_missing` et `file_too_large` pour la lecture d'un fichier
   * (ADR-0055).
   */
  readonly reason?: MotifIndisponible;
}

export type MotifIndisponible =
  | 'artifact_pruned'
  | 'repo_unknown'
  | 'object_missing'
  | 'no_previous_run'
  | 'path_missing'
  | 'file_too_large';

export type CauseErreur =
  | 'network'
  | 'unauthorized'
  | 'forbidden'
  | 'not-found'
  | 'client'
  | 'server';

export function causePour(status: number): CauseErreur {
  if (status === 0) return 'network';
  if (status === 401) return 'unauthorized';
  if (status === 403) return 'forbidden';
  if (status === 404) return 'not-found';
  if (status >= 500) return 'server';
  return 'client';
}

/** Ce qu'un appel d'API rate, sous une forme que l'interface sait afficher. */
export interface ApiError {
  readonly status: number;
  readonly message: string;
  readonly cause: CauseErreur;
}

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

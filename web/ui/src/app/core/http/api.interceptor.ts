import { HttpErrorResponse, HttpInterceptorFn } from '@angular/common/http';
import { catchError, throwError } from 'rxjs';
import { ApiError, MotifIndisponible, causePour } from './api-error';

/** Méthodes qui ne changent rien. Tout le reste est mutant. */
const SURES = new Set(['GET', 'HEAD', 'OPTIONS']);

/** L'en-tête anti-CSRF qu'attend le middleware (ADR-0035). */
const EN_TETE_UI = 'X-Warden-UI';

/**
 * Envoie le cookie de session et pose l'en-tête anti-CSRF.
 *
 * `withCredentials` est INDISPENSABLE : l'API vit sur une autre origine, et un
 * `fetch` cross-origin n'attache aucun cookie sans lui — l'utilisateur verrait
 * des 401 en étant connecté.
 *
 * L'en-tête n'est posé que sur les méthodes MUTANTES, parce que le serveur ne
 * l'exige que là et qu'un en-tête personnalisé déclenche une prévol : le poser
 * sur les `GET` doublerait chaque lecture d'un aller-retour, pour rien.
 */
export const credentialsInterceptor: HttpInterceptorFn = (req, next) => {
  const avecCookie = req.clone({
    withCredentials: true,
    setHeaders: SURES.has(req.method.toUpperCase()) ? {} : { [EN_TETE_UI]: '1' },
  });
  return next(avecCookie);
};

/**
 * Normalise les erreurs de l'API en `ApiError`.
 *
 * Le point de ce fichier : **l'API répond en `text/plain`**, pas en JSON. Elle
 * se sert de `http.Error`, qui écrit une ligne de texte nu. Un client qui
 * suppose du JSON affiche « [object Object] » ou rien, et le message réel — le
 * seul qui dise ce qui s'est passé — est perdu.
 *
 * Le corps est donc lu dans les trois formes qu'il peut prendre : texte nu,
 * objet JSON portant un `error`/`message`, ou rien du tout.
 */
export const errorInterceptor: HttpInterceptorFn = (req, next) =>
  next(req).pipe(
    catchError((err: unknown) => {
      if (!(err instanceof HttpErrorResponse)) {
        return throwError(() => err);
      }
      return throwError(() => normaliser(err));
    }),
  );

function normaliser(err: HttpErrorResponse): ApiError {
  const motif = err.headers?.get('X-Warden-Reason') ?? undefined;
  return {
    status: err.status,
    message: messageDe(err),
    cause: causePour(err.status),
    reason: (motif as MotifIndisponible | undefined) || undefined,
  };
}

function messageDe(err: HttpErrorResponse): string {
  const corps: unknown = err.error;

  if (typeof corps === 'string' && corps.trim() !== '') {
    return corps.trim();
  }
  // Angular range une erreur réseau ou un échec de parsing dans un `Error`.
  // `ProgressEvent` signale un transport tombé : le message du navigateur ne
  // dirait rien d'utile, celui-ci au moins nomme la cause probable.
  if (corps instanceof ProgressEvent || err.status === 0) {
    return 'the server could not be reached';
  }
  if (corps && typeof corps === 'object') {
    const objet = corps as Record<string, unknown>;
    for (const cle of ['error', 'message', 'detail']) {
      const valeur = objet[cle];
      if (typeof valeur === 'string' && valeur.trim() !== '') {
        return valeur.trim();
      }
    }
  }
  return err.statusText || `HTTP ${err.status}`;
}

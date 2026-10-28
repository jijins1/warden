import { inject } from '@angular/core';
import { CanActivateFn, Router, RouterStateSnapshot, UrlTree } from '@angular/router';
import { AuthService } from '../../services/auth/auth.service';

/**
 * Établit la session si elle n'est pas encore connue, et dit si elle tient.
 *
 * Extraite pour que `adminGuard` la réutilise sans passer par `authGuard` :
 * `CanActivateFn` autorise un `Observable` en retour, donc en réutiliser une
 * élargit le type au point de ne plus compiler. Le fond partagé se partage, pas
 * la signature.
 *
 * L'attente est le point : le cookie est `HttpOnly`, donc au premier rendu le
 * front ignore s'il est connecté. Répondre tout de suite renverrait vers la
 * connexion à chaque rechargement d'un lien profond — précisément le cas que
 * `GET /runs/{run_ref}` a été ajouté pour servir.
 */
async function exigerSession(state: RouterStateSnapshot): Promise<true | UrlTree> {
  const auth = inject(AuthService);
  const router = inject(Router);

  if (!auth.resolu()) {
    await auth.resoudre();
  }
  if (auth.connecte()) {
    return true;
  }
  // L'URL demandée est CONSERVÉE : sans elle, se connecter depuis un lien
  // partagé déposerait sur l'accueil, et le lien serait perdu.
  return router.createUrlTree(['/login'], { queryParams: { retour: state.url } });
}

export const authGuard: CanActivateFn = (_route, state) => exigerSession(state);

/**
 * Exige en plus le drapeau d'administration.
 *
 * Jamais implicite, et distincte : côté serveur, `CanAdministerRepos` et
 * `CanViewRun` sont deux capacités séparées — un administrateur ne gagne pas le
 * droit de lire les runs d'autrui. La garde reproduit cette séparation plutôt
 * que de la ramener à un rang unique.
 *
 * Elle ne REMPLACE pas le contrôle serveur : elle épargne un écran qui
 * répondrait 404 de toute façon.
 */
export const adminGuard: CanActivateFn = async (_route, state) => {
  const auth = inject(AuthService);
  const router = inject(Router);

  const session = await exigerSession(state);
  if (session !== true) {
    return session;
  }
  return auth.admin() ? true : router.createUrlTree(['/']);
};

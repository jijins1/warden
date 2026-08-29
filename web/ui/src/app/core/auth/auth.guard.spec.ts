import { TestBed } from '@angular/core/testing';
import { Router, RouterStateSnapshot, UrlTree, provideRouter } from '@angular/router';
import { ActivatedRouteSnapshot } from '@angular/router';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { adminGuard, authGuard } from './auth.guard';
import { AuthService } from './auth.service';
import { Whoami } from '../api/wire';

/** Double d'AuthService : ce qui compte est QUAND `resoudre` est appelée. */
class AuthDouble {
  utilisateur = vi.fn<() => Whoami | null>(() => null);
  resolu = vi.fn(() => false);
  connecte = vi.fn(() => false);
  admin = vi.fn(() => false);
  resoudre = vi.fn(async () => this.utilisateur());
}

function etat(url: string): RouterStateSnapshot {
  return { url } as RouterStateSnapshot;
}
const route = {} as ActivatedRouteSnapshot;

describe('authGuard', () => {
  let auth: AuthDouble;

  beforeEach(() => {
    auth = new AuthDouble();
    TestBed.configureTestingModule({
      providers: [provideRouter([]), { provide: AuthService, useValue: auth }],
    });
  });

  const executer = (garde: typeof authGuard, url = '/runs/01JBQ7X4M2') =>
    TestBed.runInInjectionContext(() => garde(route, etat(url))) as Promise<
      boolean | UrlTree
    >;

  // LE test de ce fichier. Le cookie est HttpOnly : au premier rendu le front
  // ignore s'il est connecté. Une garde qui répondrait sans attendre
  // renverrait vers la connexion à chaque rechargement d'un lien profond.
  it('attend la résolution avant de trancher', async () => {
    auth.resolu.mockReturnValue(false);
    auth.resoudre.mockImplementation(async () => {
      auth.connecte.mockReturnValue(true);
      auth.resolu.mockReturnValue(true);
      return { subject: 'alice' };
    });

    await expect(executer(authGuard)).resolves.toBe(true);
    expect(auth.resoudre).toHaveBeenCalledOnce();
  });

  // Une fois résolue, on ne redemande pas au serveur à chaque navigation.
  it('ne rappelle pas le serveur quand la session est déjà connue', async () => {
    auth.resolu.mockReturnValue(true);
    auth.connecte.mockReturnValue(true);

    await expect(executer(authGuard)).resolves.toBe(true);
    expect(auth.resoudre).not.toHaveBeenCalled();
  });

  // Sans l'URL de retour, se connecter depuis un lien partagé déposerait sur
  // l'accueil et le lien serait perdu.
  it('conserve l’URL demandée en repoussant vers la connexion', async () => {
    auth.resolu.mockReturnValue(true);
    auth.connecte.mockReturnValue(false);

    const resultat = await executer(authGuard, '/runs/01JBQ7X4M2');
    const router = TestBed.inject(Router);

    expect(resultat).toBeInstanceOf(UrlTree);
    expect(router.serializeUrl(resultat as UrlTree)).toBe(
      '/login?retour=%2Fruns%2F01JBQ7X4M2',
    );
  });
});

describe('adminGuard', () => {
  let auth: AuthDouble;

  beforeEach(() => {
    auth = new AuthDouble();
    TestBed.configureTestingModule({
      providers: [provideRouter([]), { provide: AuthService, useValue: auth }],
    });
  });

  const executer = () =>
    TestBed.runInInjectionContext(() => adminGuard(route, etat('/admin/users'))) as Promise<
      boolean | UrlTree
    >;

  it('laisse passer un administrateur', async () => {
    auth.resolu.mockReturnValue(true);
    auth.connecte.mockReturnValue(true);
    auth.admin.mockReturnValue(true);

    await expect(executer()).resolves.toBe(true);
  });

  // Connecté mais pas administrateur : on renvoie à l'accueil, PAS à la
  // connexion — se reconnecter ne changerait rien au droit manquant.
  it('renvoie un simple connecté vers l’accueil', async () => {
    auth.resolu.mockReturnValue(true);
    auth.connecte.mockReturnValue(true);
    auth.admin.mockReturnValue(false);

    const resultat = await executer();
    const router = TestBed.inject(Router);
    expect(router.serializeUrl(resultat as UrlTree)).toBe('/');
  });

  it('renvoie un anonyme vers la connexion', async () => {
    auth.resolu.mockReturnValue(true);
    auth.connecte.mockReturnValue(false);

    const resultat = await executer();
    const router = TestBed.inject(Router);
    expect(router.serializeUrl(resultat as UrlTree)).toContain('/login');
  });
});

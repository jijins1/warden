import { TestBed } from '@angular/core/testing';
import { provideHttpClient, withInterceptors } from '@angular/common/http';
import {
  HttpTestingController,
  provideHttpClientTesting,
} from '@angular/common/http/testing';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { AuthService } from './auth.service';
import { APP_CONFIG } from '../config/app-config';
import { credentialsInterceptor, errorInterceptor } from '../http/api.interceptor';

describe('AuthService', () => {
  let auth: AuthService;
  let ctrl: HttpTestingController;

  beforeEach(() => {
    TestBed.configureTestingModule({
      providers: [
        // Les MÊMES interceptors qu'en production : sans eux, le service verrait
        // un `HttpErrorResponse` brut là où il reçoit un `ApiError` normalisé,
        // et le test validerait une chaîne qui n'existe pas.
        provideHttpClient(withInterceptors([credentialsInterceptor, errorInterceptor])),
        provideHttpClientTesting(),
        { provide: APP_CONFIG, useValue: { apiUrl: 'https://api.test' } },
      ],
    });
    auth = TestBed.inject(AuthService);
    ctrl = TestBed.inject(HttpTestingController);
  });

  afterEach(() => ctrl.verify());

  // Le cookie est HttpOnly : ne pas être connecté au démarrage est le cas
  // NOMINAL, pas une panne. Propager l'erreur empêcherait l'application de
  // démarrer sur son écran de connexion.
  it('traite un whoami en échec comme « personne »', async () => {
    const p = auth.resoudre();
    ctrl.expectOne('https://api.test/auth/whoami').flush('authentication required', {
      status: 401,
      statusText: '',
    });

    await expect(p).resolves.toBeNull();
    expect(auth.connecte()).toBe(false);
    expect(auth.resolu()).toBe(true);
  });

  it('retient l’identité rendue par le serveur', async () => {
    const p = auth.resoudre();
    ctrl.expectOne('https://api.test/auth/whoami').flush({ subject: 'alice', admin: true });

    await p;
    expect(auth.connecte()).toBe(true);
    expect(auth.admin()).toBe(true);
  });

  describe('changement de mot de passe', () => {
    async function connecter() {
      const p = auth.resoudre();
      ctrl.expectOne('https://api.test/auth/whoami').flush({ subject: 'alice' });
      await p;
    }

    // Le serveur révoque TOUTES les sessions du compte dans la même
    // transaction, celle-ci comprise. L'état local doit tomber avec.
    it('déconnecte après un succès', async () => {
      await connecter();
      expect(auth.connecte()).toBe(true);

      const p = auth.changerMotDePasse('vieux', 'nouveau-assez-long');
      ctrl.expectOne('https://api.test/auth/password').flush('', { status: 204, statusText: '' });
      await p;

      expect(auth.connecte()).toBe(false);
    });

    // LE cas qui compte. Sur un refus, la session courante peut déjà être
    // morte : rester « connecté » localement ferait découvrir des 401 à la
    // requête suivante, sans que rien n'explique pourquoi.
    it('déconnecte aussi quand le serveur refuse', async () => {
      await connecter();

      const p = auth.changerMotDePasse('faux', 'nouveau-assez-long');
      ctrl.expectOne('https://api.test/auth/password').flush('invalid credentials', {
        status: 401,
        statusText: '',
      });

      await expect(p).rejects.toBeTruthy();
      expect(auth.connecte()).toBe(false);
    });

    // Le 400 porte la règle de robustesse : c'est le seul message que le
    // serveur veut précis, et il doit remonter tel quel jusqu'à l'écran.
    it('laisse remonter le motif d’un mot de passe refusé', async () => {
      await connecter();

      const p = auth.changerMotDePasse('vieux', 'court');
      ctrl.expectOne('https://api.test/auth/password').flush(
        'mot de passe trop court : 12 caractères au minimum',
        { status: 400, statusText: '' },
      );

      await expect(p).rejects.toMatchObject({
        status: 400,
        message: 'mot de passe trop court : 12 caractères au minimum',
      });
    });
  });
});

import { TestBed } from '@angular/core/testing';
import { HttpClient, provideHttpClient, withInterceptors } from '@angular/common/http';
import {
  HttpTestingController,
  provideHttpClientTesting,
} from '@angular/common/http/testing';
import { beforeEach, describe, expect, it, afterEach } from 'vitest';
import { Observable } from 'rxjs';
import { credentialsInterceptor, errorInterceptor } from './api.interceptor';
import { ApiError } from '../../models/api-error';

describe('api.interceptor', () => {
  let http: HttpClient;
  let ctrl: HttpTestingController;

  beforeEach(() => {
    TestBed.configureTestingModule({
      providers: [
        provideHttpClient(withInterceptors([credentialsInterceptor, errorInterceptor])),
        provideHttpClientTesting(),
      ],
    });
    http = TestBed.inject(HttpClient);
    ctrl = TestBed.inject(HttpTestingController);
  });

  afterEach(() => ctrl.verify());

  describe('credentials', () => {
    // Sans `withCredentials`, un `fetch` cross-origin n'attache AUCUN cookie :
    // l'utilisateur verrait des 401 en étant connecté.
    it('envoie le cookie sur toute requête', () => {
      http.get('/runs').subscribe();
      const req = ctrl.expectOne('/runs');
      expect(req.request.withCredentials).toBe(true);
      req.flush({});
    });

    // L'en-tête anti-CSRF n'est exigé QUE sur les méthodes mutantes, et un
    // en-tête personnalisé déclenche une prévol : le poser sur les lectures
    // doublerait chaque appel d'un aller-retour, pour rien.
    it('ne pose pas X-Warden-UI sur une lecture', () => {
      http.get('/runs').subscribe();
      const req = ctrl.expectOne('/runs');
      expect(req.request.headers.has('X-Warden-UI')).toBe(false);
      req.flush({});
    });

    it('pose X-Warden-UI sur une écriture', () => {
      http.post('/auth/logout', null).subscribe();
      const req = ctrl.expectOne('/auth/logout');
      expect(req.request.headers.get('X-Warden-UI')).toBe('1');
      req.flush({});
    });
  });

  describe('normalisation des erreurs', () => {
    // LE test de ce fichier. L'API répond en `text/plain` via `http.Error` ;
    // un client qui suppose du JSON perd le seul message qui dise ce qui s'est
    // passé.
    it('lit un corps text/plain', async () => {
      const promesse = attendreErreur(http.get('/runs'));
      ctrl.expectOne('/runs').flush('run not found', {
        status: 404,
        statusText: 'Not Found',
      });

      const err = await promesse;
      expect(err.message).toBe('run not found');
      expect(err.status).toBe(404);
      expect(err.cause).toBe('not-found');
    });

    it('lit un corps JSON portant `error`', async () => {
      const promesse = attendreErreur(http.get('/runs'));
      ctrl.expectOne('/runs').flush(
        { error: 'invalid cursor' },
        { status: 400, statusText: 'Bad Request' },
      );

      const err = await promesse;
      expect(err.message).toBe('invalid cursor');
      expect(err.cause).toBe('client');
    });

    it('nomme la panne de transport plutôt que de rendre un objet vide', async () => {
      const promesse = attendreErreur(http.get('/runs'));
      ctrl.expectOne('/runs').error(new ProgressEvent('error'), { status: 0 });

      const err = await promesse;
      expect(err.cause).toBe('network');
      expect(err.message).toBe('the server could not be reached');
    });

    it('classe 401 et 403 séparément', async () => {
      const p401 = attendreErreur(http.get('/a'));
      ctrl.expectOne('/a').flush('authentication required', { status: 401, statusText: '' });
      expect((await p401).cause).toBe('unauthorized');

      const p403 = attendreErreur(http.get('/b'));
      ctrl.expectOne('/b').flush('missing X-Warden-UI header', { status: 403, statusText: '' });
      const err = await p403;
      expect(err.cause).toBe('forbidden');
      // Le message du serveur passe intact : c'est lui qui dit quoi corriger.
      expect(err.message).toBe('missing X-Warden-UI header');
    });

    // Un corps vide arrive sur les 204 et sur certains 500. Le message ne doit
    // pas devenir « [object Object] » ni la chaîne vide.
    it('se rabat sur le statut quand le corps est vide', async () => {
      const promesse = attendreErreur(http.get('/runs'));
      ctrl.expectOne('/runs').flush('', { status: 500, statusText: 'Internal Server Error' });

      const err = await promesse;
      expect(err.cause).toBe('server');
      expect(err.message).toBe('Internal Server Error');
    });
  });
});

/** Capte l'erreur d'un observable, typée. */
function attendreErreur(obs: Observable<unknown>): Promise<ApiError> {
  return new Promise<ApiError>((resolve, reject) => {
    obs.subscribe({
      next: () => reject(new Error('la requête a réussi, une erreur était attendue')),
      error: (e: ApiError) => resolve(e),
    });
  });
}

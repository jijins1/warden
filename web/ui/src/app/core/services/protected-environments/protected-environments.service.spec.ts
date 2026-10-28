import { TestBed } from '@angular/core/testing';
import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { ProtectedEnvironmentsService } from './protected-environments.service';
import { APP_CONFIG } from '../../app-config/app-config';

describe('ProtectedEnvironmentsService', () => {
  let service: ProtectedEnvironmentsService;
  let ctrl: HttpTestingController;

  beforeEach(() => {
    TestBed.configureTestingModule({
      providers: [
        provideHttpClient(),
        provideHttpClientTesting(),
        { provide: APP_CONFIG, useValue: { apiUrl: 'https://api.test' } },
      ],
    });
    service = TestBed.inject(ProtectedEnvironmentsService);
    ctrl = TestBed.inject(HttpTestingController);
  });

  afterEach(() => ctrl.verify());

  it('marque protégé un environnement déclaré par le serveur', async () => {
    service.charger();
    ctrl.expectOne('https://api.test/environments/protected').flush({
      environments: ['preprod'],
    });
    await Promise.resolve();

    expect(service.isProtected('preprod')).toBe(true);
    expect(service.isProtected('PREPROD')).toBe(true);
    expect(service.isProtected('prod')).toBe(false);
  });

  it("ne réémet pas de requête si déjà chargé", () => {
    service.charger();
    ctrl.expectOne('https://api.test/environments/protected').flush({ environments: [] });

    service.charger();
    ctrl.expectNone('https://api.test/environments/protected');
  });

  // Un échec vaut « rien de connu », pas une panne : voir le commentaire du
  // service.
  it('traite un échec réseau comme une liste vide', async () => {
    service.charger();
    ctrl.expectOne('https://api.test/environments/protected').flush('boom', {
      status: 500,
      statusText: '',
    });
    await Promise.resolve();

    expect(service.isProtected('prod')).toBe(false);
  });
});

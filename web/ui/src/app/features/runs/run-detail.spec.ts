import { Component } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { describe, expect, it, vi } from 'vitest';
import { RunDetail } from './run-detail';
import { RunsService } from '../../core/api/runs.service';
import { APP_CONFIG } from '../../core/config/app-config';
import { provideRouter } from '@angular/router';
import { provideHttpClient } from '@angular/common/http';
import { provideHttpClientTesting } from '@angular/common/http/testing';

@Component({ imports: [RunDetail], template: '<w-run-detail [runRef]="ref" />' })
class Hote {
  ref = '01J';
}

const unRun = (ref: string) => ({
  run_ref: ref,
  environment: 'prod',
  state: 'succeeded',
  playbook: 'play.yml',
  author: { subject: 'alice' },
});

function monter(parReference: (r: string) => Promise<unknown>) {
  const service = { parReference: vi.fn(parReference) };
  TestBed.configureTestingModule({
    providers: [
      { provide: RunsService, useValue: service },
      { provide: APP_CONFIG, useValue: { apiUrl: 'https://api.invalid' } },
      provideRouter([]),
      // `w-env-badge` demande désormais `ProtectedEnvironmentsService`, qui
      // demande `HttpClient` (ADR-0044).
      provideHttpClient(),
      provideHttpClientTesting(),
    ],
  });
  return { service, fixture: TestBed.createComponent(Hote) };
}

describe('RunDetail', () => {
  // NG0950. Une entrée `input.required()` n'est posée qu'APRÈS la construction :
  // la lire depuis le constructeur lève, et l'écran entier tombe sur un bandeau
  // rouge alors que le run est parfaitement lisible.
  it('ne lit pas runRef avant que l’entrée soit posée', async () => {
    const { service, fixture } = monter((r) => Promise.resolve(unRun(r)));
    await fixture.whenStable();

    expect(service.parReference).toHaveBeenCalledWith('01J');
  });

  // Le composant est RÉUTILISÉ d'un run à l'autre par le routeur : sans réaction
  // au changement d'entrée, la page afficherait le run précédent.
  it('recharge quand la référence change', async () => {
    const { service, fixture } = monter((r) => Promise.resolve(unRun(r)));
    await fixture.whenStable();

    fixture.componentInstance.ref = '01K';
    fixture.detectChanges();
    await fixture.whenStable();

    expect(service.parReference).toHaveBeenLastCalledWith('01K');
  });
});

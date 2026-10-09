import { Component } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { ActivatedRoute } from '@angular/router';
import { provideRouter } from '@angular/router';
import { provideHttpClient } from '@angular/common/http';
import { provideHttpClientTesting } from '@angular/common/http/testing';
import { of } from 'rxjs';
import { describe, expect, it, vi } from 'vitest';
import { HostRuns } from './host-runs';
import { RunsService } from '../../core/api/runs.service';
import { APP_CONFIG } from '../../core/config/app-config';

@Component({ imports: [HostRuns], template: '<w-host-runs />' })
class Hote {}

const unPlaybook = (playbook: string, runRef: string) => ({
  Repo: 'warden',
  Playbook: playbook,
  Environment: 'prod',
  Inventory: 'inv/prod',
  LastRunRef: runRef,
  LastUserID: 1,
  LastAuditSHA: 'a'.repeat(40),
  LastState: 'succeeded',
  LastStartedAt: new Date().toISOString(),
});

function monter(opts: {
  playbooksParMachine?: () => Promise<unknown>;
  parMachine?: () => Promise<unknown>;
}) {
  const service = {
    playbooksParMachine: vi.fn(opts.playbooksParMachine ?? (() => Promise.resolve([]))),
    parMachine: vi.fn(opts.parMachine ?? (() => Promise.resolve([]))),
  };
  const route = {
    params: of({ host: 'db-01' }),
    queryParams: of({}),
  } as unknown as ActivatedRoute;

  TestBed.configureTestingModule({
    providers: [
      { provide: RunsService, useValue: service },
      { provide: APP_CONFIG, useValue: { apiUrl: 'https://api.invalid' } },
      provideRouter([]),
      provideHttpClient(),
      provideHttpClientTesting(),
      // APRÈS provideRouter([]) : sinon son propre ActivatedRoute (celui de la
      // route racine, sans `host`) l'emporte en étant déclaré plus tard.
      { provide: ActivatedRoute, useValue: route },
    ],
  });
  return { service, fixture: TestBed.createComponent(Hote) };
}

describe('HostRuns (écran détail machine)', () => {
  // L'onglet Playbooks est la vue par défaut : « quels playbooks ont tourné
  // ici » plutôt que « quels runs ».
  it('charge les playbooks par défaut, sans charger les runs en plus', async () => {
    const { service, fixture } = monter({});
    await fixture.whenStable();

    expect(service.playbooksParMachine).toHaveBeenCalledWith('db-01', undefined, undefined);
  });

  it('bascule sur l’onglet Runs au clic', async () => {
    const { service, fixture } = monter({
      parMachine: () => Promise.resolve([]),
    });
    await fixture.whenStable();

    const el = fixture.nativeElement as HTMLElement;
    const bouton = Array.from(el.querySelectorAll<HTMLButtonElement>('.onglets button')).find(
      (b) => b.textContent?.trim() === 'Runs',
    );
    bouton?.click();
    fixture.detectChanges();
    await fixture.whenStable();

    expect(service.parMachine).toHaveBeenCalledWith('db-01', undefined, undefined);
    expect(el.textContent).not.toContain('No playbook visible');
  });

  // Sélectionner une ligne de playbook ouvre le diff sur place — pas de
  // navigation — et une seconde sélection le referme plutôt que d'en ouvrir un
  // second : un seul diff à la fois.
  it('ouvre et referme le diff au clic sur une ligne de playbook', async () => {
    const { fixture } = monter({
      playbooksParMachine: () => Promise.resolve([unPlaybook('deploy.yml', '01JRUN0000000000000000001')]),
    });
    await fixture.whenStable();
    const el = fixture.nativeElement as HTMLElement;

    expect(el.querySelector('w-run-diff')).toBeNull();

    const ligne = el.querySelector<HTMLButtonElement>('button.ligne[role="row"]');
    ligne?.click();
    fixture.detectChanges();
    await fixture.whenStable();
    expect(el.querySelector('w-run-diff')).not.toBeNull();

    ligne?.click();
    fixture.detectChanges();
    await fixture.whenStable();
    expect(el.querySelector('w-run-diff')).toBeNull();
  });
});

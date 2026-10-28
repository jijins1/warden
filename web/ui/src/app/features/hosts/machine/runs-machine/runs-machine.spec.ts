import { TestBed } from '@angular/core/testing';
import { provideRouter } from '@angular/router';
import { describe, expect, it, vi } from 'vitest';
import { RunsMachine } from './runs-machine';
import { RunsService } from '../../../../core/services/runs/runs.service';
import { ProtectedEnvironmentsService } from '../../../../core/services/protected-environments/protected-environments.service';

const unRun = (i: number) => ({
  RunRef: `01JRUN${String(i).padStart(20, '0')}`,
  UserID: 1,
  Author: { Subject: 'ruokki', DisplayName: '' },
  Repo: 'warden',
  Environment: 'prod',
  Inventory: 'inv/prod',
  Playbook: 'site.yml',
  State: 'succeeded',
  TerminationCause: '',
  StartedAt: new Date().toISOString(),
  EndedAt: new Date().toISOString(),
  ChangedCount: 0,
  SecretsUnmasked: false,
});

async function monter(nombre: number) {
  const service = {
    parMachine: vi.fn(() => Promise.resolve(Array.from({ length: nombre }, (_, i) => unRun(i)))),
  };
  TestBed.configureTestingModule({
    providers: [
      { provide: RunsService, useValue: service },
      { provide: ProtectedEnvironmentsService, useValue: { charger: vi.fn(), isProtected: () => false } },
      provideRouter([]),
    ],
  });
  const fixture = TestBed.createComponent(RunsMachine);
  fixture.componentRef.setInput('host', 'db-01');
  fixture.componentRef.setInput('since', '2026-10-01T00:00:00.000Z');
  await fixture.whenStable();
  return { service, el: fixture.nativeElement as HTMLElement };
}

describe('RunsMachine', () => {
  it('demande les runs de la machine sur la fenêtre reçue', async () => {
    const { service } = await monter(1);
    expect(service.parMachine).toHaveBeenCalledWith('db-01', '2026-10-01T00:00:00.000Z', undefined);
  });

  // Le serveur plafonne à 500 et ne dit PAS qu'il a tronqué : une réponse
  // pleine doit le dire, sans quoi on conclurait qu'il n'y a rien d'autre.
  it('signale le plafond quand la réponse en atteint la taille', async () => {
    const { el } = await monter(500);
    expect(el.querySelector('.w-callout--cap')?.textContent).toContain('maximum of 500');
  });

  it('ne signale rien sous le plafond', async () => {
    const { el } = await monter(499);
    expect(el.querySelector('.w-callout--cap')).toBeNull();
  });
});

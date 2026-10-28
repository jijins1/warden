import { TestBed } from '@angular/core/testing';
import { provideRouter } from '@angular/router';
import { describe, expect, it, vi } from 'vitest';
import { TableRuns } from './table-runs';
import { RunListItem } from '../../core/models/wire';
import { ProtectedEnvironmentsService } from '../../core/services/protected-environments/protected-environments.service';

function run(partiel: Partial<RunListItem>): RunListItem {
  return {
    RunRef: '01J0000000000000000000ABCD',
    Author: { Subject: 'alice', DisplayName: '' },
    Repo: 'demo',
    Environment: 'prod',
    Inventory: '',
    Playbook: 'site.yml',
    State: 'succeeded',
    TerminationCause: '',
    StartedAt: '2026-10-28T10:00:00Z',
    EndedAt: '2026-10-28T10:01:00Z',
    ChangedCount: 0,
    SecretsUnmasked: false,
    ...partiel,
  };
}

async function rendre(runs: RunListItem[]) {
  TestBed.configureTestingModule({
    providers: [
      provideRouter([]),
      { provide: ProtectedEnvironmentsService, useValue: { charger: vi.fn(), isProtected: () => false } },
    ],
  });
  const fixture = TestBed.createComponent(TableRuns);
  fixture.componentRef.setInput('runs', runs);
  await fixture.whenStable();
  return fixture.nativeElement as HTMLElement;
}

describe('TableRuns', () => {
  it('fait de chaque ligne un lien vers son run', async () => {
    const el = await rendre([run({})]);
    const lien = el.querySelector('a.w-tr');
    expect(lien?.getAttribute('href')).toBe('/runs/01J0000000000000000000ABCD');
  });

  // Un run_ref tronqué sous 10 caractères devient ambigu.
  it('tronque le run_ref à 10 caractères, la valeur complète au survol', async () => {
    const ref = (await rendre([run({})])).querySelector('.w-ref')!;
    expect(ref.textContent?.trim()).toBe('01J0000000');
    expect(ref.getAttribute('title')).toBe('01J0000000000000000000ABCD');
  });

  it('signale une rédaction échouée', async () => {
    const el = await rendre([run({ SecretsUnmasked: true })]);
    expect(el.querySelector('.secrets')).not.toBeNull();
  });
});

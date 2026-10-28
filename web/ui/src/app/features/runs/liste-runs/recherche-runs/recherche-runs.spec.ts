import { Component, signal } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { Router, provideRouter } from '@angular/router';
import { describe, expect, it, vi } from 'vitest';
import { RechercheRuns } from './recherche-runs';
import { FiltreRuns, RunsService } from '../../../../core/services/runs/runs.service';
import { EnvironmentsService } from '../../../../core/services/environments/environments.service';
import { ProtectedEnvironmentsService } from '../../../../core/services/protected-environments/protected-environments.service';
import { RunListItem, RunListResult } from '../../../../core/models/wire';

@Component({
  imports: [RechercheRuns],
  template: '<w-recherche-runs [filtre]="filtre()" />',
})
class Hote {
  readonly filtre = signal<FiltreRuns>({});
}

function run(ref: string): RunListItem {
  return {
    RunRef: ref,
    Playbook: 'site.yml',
    Environment: 'prod',
    State: 'succeeded',
    ChangedCount: 0,
    StartedAt: '2026-10-28T10:00:00Z',
    Author: { Subject: 'u', DisplayName: 'U' },
  } as RunListItem;
}

function page(refs: string[], suite = ''): RunListResult {
  return { Runs: refs.map(run), NextCursor: suite };
}

async function monter(options: { environnements?: () => Promise<string[]> } = {}) {
  const runs = { lister: vi.fn().mockResolvedValue(page(['01A'])) };
  const environnements = {
    mesEnvironnements: vi.fn(options.environnements ?? (() => Promise.resolve(['prod', 'staging']))),
  };
  TestBed.configureTestingModule({
    providers: [
      provideRouter([]),
      { provide: RunsService, useValue: runs },
      { provide: EnvironmentsService, useValue: environnements },
      { provide: ProtectedEnvironmentsService, useValue: { charger: vi.fn(), isProtected: () => false } },
    ],
  });
  const navigate = vi.spyOn(TestBed.inject(Router), 'navigate').mockResolvedValue(true);
  const fixture = TestBed.createComponent(Hote);
  await fixture.whenStable();
  const el = fixture.nativeElement as HTMLElement;
  return { runs, navigate, fixture, el };
}

function bouton(el: HTMLElement, texte: string): HTMLButtonElement {
  const trouve = Array.from(el.querySelectorAll('button')).find(
    (b) => b.textContent?.trim() === texte,
  );
  if (!trouve) throw new Error(`pas de bouton « ${texte} »`);
  return trouve;
}

describe('RechercheRuns', () => {
  it('recharge quand les filtres changent, sans garder la page précédente', async () => {
    const { runs, fixture, el } = await monter();
    expect(runs.lister).toHaveBeenCalledWith({}, undefined);

    runs.lister.mockResolvedValue(page(['01B', '01C']));
    fixture.componentInstance.filtre.set({ state: 'failed' });
    await fixture.whenStable();

    expect(runs.lister).toHaveBeenLastCalledWith({ state: 'failed' }, undefined);
    expect(el.textContent).toContain('2 shown');
  });

  it('« Load more » passe le curseur et ajoute à la suite', async () => {
    const { runs, fixture, el } = await monter();
    runs.lister.mockResolvedValue(page(['01A'], 'curseur-1'));
    fixture.componentInstance.filtre.set({ env: 'prod' });
    await fixture.whenStable();

    runs.lister.mockResolvedValue(page(['01B']));
    bouton(el, 'Load more').click();
    await fixture.whenStable();

    expect(runs.lister).toHaveBeenLastCalledWith({ env: 'prod' }, 'curseur-1');
    expect(el.textContent).toContain('2 shown');
    expect(el.textContent).toContain('End of results');
  });

  it('écrit les filtres dans l’URL au lieu de recharger lui-même', async () => {
    const { runs, navigate, el } = await monter();
    runs.lister.mockClear();

    bouton(el, 'Changed only').click();
    expect(navigate).toHaveBeenCalledWith([], {
      queryParams: { changed_min: 1 },
      queryParamsHandling: 'merge',
    });
    expect(runs.lister).not.toHaveBeenCalled();
  });

  it('un filtre posé se rebascule à vide', async () => {
    const { navigate, fixture, el } = await monter();
    fixture.componentInstance.filtre.set({ mine: 1 });
    await fixture.whenStable();

    const miens = bouton(el, 'Mine only');
    expect(miens.getAttribute('aria-pressed')).toBe('true');
    miens.click();
    expect(navigate).toHaveBeenCalledWith([], {
      queryParams: { mine: null },
      queryParamsHandling: 'merge',
    });
  });

  it('« Clear filters » retire tous les filtres de l’URL', async () => {
    const { navigate, fixture, el } = await monter();
    fixture.componentInstance.filtre.set({ env: 'prod', changed_min: 1 });
    await fixture.whenStable();

    bouton(el, 'Clear filters').click();
    expect(navigate).toHaveBeenCalledWith([], {
      queryParams: {
        env: null,
        inventory: null,
        state: null,
        since: null,
        until: null,
        changed_min: null,
        mine: null,
      },
      queryParamsHandling: 'merge',
    });
  });

  it('montre l’erreur du serveur avec « Retry »', async () => {
    const { runs, fixture, el } = await monter();
    runs.lister.mockRejectedValue({ message: 'boom' });
    fixture.componentInstance.filtre.set({ state: 'failed' });
    await fixture.whenStable();

    expect(el.querySelector('[role="alert"]')?.textContent).toContain('boom');
    runs.lister.mockResolvedValue(page([]));
    bouton(el, 'Retry').click();
    await fixture.whenStable();
    expect(el.querySelector('[role="alert"]')).toBeNull();
    expect(el.textContent).toContain('No run matches.');
  });

  it('un échec des environnements retire le filtre, pas la liste', async () => {
    const { el } = await monter({ environnements: () => Promise.reject(new Error('403')) });

    expect(el.querySelector('[aria-label="Environment"]')).toBeNull();
    expect(el.querySelector('[role="alert"]')).toBeNull();
    expect(el.textContent).toContain('1 shown');
  });
});

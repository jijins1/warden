import { TestBed } from '@angular/core/testing';
import { provideRouter } from '@angular/router';
import { provideHttpClient } from '@angular/common/http';
import { provideHttpClientTesting } from '@angular/common/http/testing';
import { describe, expect, it, vi } from 'vitest';
import { Machine } from './machine';
import { RunsService } from '../../../core/services/runs/runs.service';
import { APP_CONFIG } from '../../../core/app-config/app-config';

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

  TestBed.configureTestingModule({
    providers: [
      { provide: RunsService, useValue: service },
      { provide: APP_CONFIG, useValue: { apiUrl: 'https://api.invalid' } },
      provideRouter([]),
      provideHttpClient(),
      provideHttpClientTesting(),
    ],
  });
  const fixture = TestBed.createComponent(Machine);
  // Ce que `withComponentInputBinding` fait depuis `/hosts/db-01`.
  fixture.componentRef.setInput('host', 'db-01');
  return { service, fixture };
}

function bouton(el: HTMLElement, texte: string): HTMLButtonElement | undefined {
  return Array.from(el.querySelectorAll<HTMLButtonElement>('button')).find(
    (b) => b.textContent?.trim() === texte,
  );
}

describe('Machine (écran détail machine)', () => {
  it('titre la page du nom de la machine, avec le retour vers la liste', async () => {
    const { fixture } = monter({});
    await fixture.whenStable();
    const el = fixture.nativeElement as HTMLElement;

    expect(el.querySelector('h1')?.textContent?.trim()).toBe('db-01');
    expect(el.querySelector('a.w-back')?.getAttribute('href')).toBe('/hosts');
    // Plus de saisie : on choisit la machine dans la liste.
    expect(el.querySelector('input')).toBeNull();
  });

  // La vue Playbooks est la vue par défaut : « quels playbooks ont tourné
  // ici » plutôt que « quels runs ».
  it('charge les playbooks par défaut, et les montre', async () => {
    const { service, fixture } = monter({});
    await fixture.whenStable();

    expect(service.playbooksParMachine).toHaveBeenCalledWith('db-01', undefined, undefined);
    expect((fixture.nativeElement as HTMLElement).textContent).toContain('No playbook visible');
  });

  it('bascule sur la vue Runs au clic', async () => {
    const { service, fixture } = monter({
      parMachine: () => Promise.resolve([]),
    });
    await fixture.whenStable();

    const el = fixture.nativeElement as HTMLElement;
    bouton(el, 'Runs')?.click();
    fixture.detectChanges();
    await fixture.whenStable();

    expect(service.parMachine).toHaveBeenCalledWith('db-01', undefined, undefined);
    expect(el.textContent).not.toContain('No playbook visible');
    expect(el.textContent).toContain('No run visible');
  });

  // Sélectionner une ligne de playbook ouvre le diff sur place — pas de
  // navigation — et une seconde sélection le referme plutôt que d'en ouvrir un
  // second : un seul diff à la fois.
  it('ouvre et referme le diff au clic sur une ligne de playbook', async () => {
    const { fixture } = monter({
      playbooksParMachine: () =>
        Promise.resolve([
          unPlaybook('deploy.yml', '01JRUN0000000000000000001'),
          unPlaybook('site.yml', '01JRUN0000000000000000002'),
        ]),
    });
    await fixture.whenStable();
    const el = fixture.nativeElement as HTMLElement;

    expect(el.querySelector('w-run-diff')).toBeNull();

    const [premiere, seconde] = Array.from(
      el.querySelectorAll<HTMLButtonElement>('.w-xrow > button.w-tr'),
    );
    premiere.click();
    fixture.detectChanges();
    await fixture.whenStable();
    expect(el.querySelectorAll('w-run-diff').length).toBe(1);
    expect(premiere.getAttribute('aria-expanded')).toBe('true');

    seconde.click();
    fixture.detectChanges();
    await fixture.whenStable();
    expect(el.querySelectorAll('w-run-diff').length).toBe(1);
    expect(premiere.getAttribute('aria-expanded')).toBe('false');

    seconde.click();
    fixture.detectChanges();
    await fixture.whenStable();
    expect(el.querySelector('w-run-diff')).toBeNull();
  });
});

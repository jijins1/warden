import { Component } from '@angular/core';
import { ComponentFixture, TestBed } from '@angular/core/testing';
import { describe, expect, it, vi } from 'vitest';
import { ExplorateurRun } from './explorateur-run';
import { DiffsService } from '../../../core/services/diffs/diffs.service';
import { ApiError } from '../../../core/models/api-error';

const DIFF = [
  'diff --git a/roles/web/tasks/main.yml b/roles/web/tasks/main.yml',
  '--- a/roles/web/tasks/main.yml',
  '+++ b/roles/web/tasks/main.yml',
  '@@ -1 +1 @@',
  '-a',
  '+b',
  '',
].join('\n');

const ARBRE = {
  files: [
    { path: 'roles/web/tasks/main.yml', size: 2 },
    { path: 'roles/web/handlers/main.yml', size: 30 },
    { path: 'site.yml', size: 40 },
  ],
  truncated: false,
};

@Component({ imports: [ExplorateurRun], template: '<w-explorateur-run runRef="01J" [diff]="diff" />' })
class Hote {
  diff = DIFF;
}

function monter(service: Partial<Record<keyof DiffsService, unknown>>) {
  TestBed.configureTestingModule({ providers: [{ provide: DiffsService, useValue: service }] });
  return TestBed.createComponent(Hote);
}

async function cliquer(fixture: ComponentFixture<Hote>, selecteur: string, texte: string) {
  const el = fixture.nativeElement as HTMLElement;
  const bouton = Array.from(el.querySelectorAll<HTMLButtonElement>(selecteur)).find((b) => b.textContent?.includes(texte));
  bouton!.click();
  await fixture.whenStable();
}

const libelles = (el: HTMLElement) =>
  Array.from(el.querySelectorAll('.w-arbre-ligne')).map((b) => b.querySelector('.w-truncate')?.textContent?.trim());

describe('ExplorateurRun', () => {
  // L'arbre coûte un aller au rôle `git` : il ne se charge qu'à la demande.
  it('ne charge l’arbre qu’à la bascule sur « All files »', async () => {
    const service = { arbreDuRun: vi.fn(() => Promise.resolve(ARBRE)), fichierDuRun: vi.fn() };
    const fixture = monter(service);
    await fixture.whenStable();
    const el = fixture.nativeElement as HTMLElement;
    expect(service.arbreDuRun).not.toHaveBeenCalled();
    expect(libelles(el)).toEqual(['roles/web/tasks', 'main.yml']);

    await cliquer(fixture, '.w-seg button', 'All files');

    expect(service.arbreDuRun).toHaveBeenCalledWith('01J');
    // Seul le dossier qui contient une modification s'ouvre d'office.
    expect(libelles(el)).toEqual(['roles/web', 'handlers', 'tasks', 'main.yml', 'site.yml']);
  });

  it('lit un fichier intact à la demande, numéroté', async () => {
    const service = {
      arbreDuRun: vi.fn(() => Promise.resolve(ARBRE)),
      fichierDuRun: vi.fn(() => Promise.resolve('- hosts: all\n  roles: [web]\n')),
    };
    const fixture = monter(service);
    await fixture.whenStable();
    await cliquer(fixture, '.w-seg button', 'All files');
    await cliquer(fixture, '.w-arbre-ligne', 'site.yml');

    expect(service.fichierDuRun).toHaveBeenCalledWith('01J', 'site.yml');
    const el = fixture.nativeElement as HTMLElement;
    expect(el.querySelector('w-contenu-fichier .w-fichier-diff-tete')?.textContent).toContain('site.yml');
    expect(el.querySelectorAll('w-contenu-fichier tr')).toHaveLength(2);
  });

  // Un fichier modifié s'affiche par son diff, déjà là : aucun aller de plus.
  it('n’appelle pas le serveur pour un fichier modifié', async () => {
    const service = { arbreDuRun: vi.fn(() => Promise.resolve(ARBRE)), fichierDuRun: vi.fn() };
    const fixture = monter(service);
    await fixture.whenStable();
    await cliquer(fixture, '.w-seg button', 'All files');
    await cliquer(fixture, '.w-arbre-ligne', 'main.yml');

    expect(service.fichierDuRun).not.toHaveBeenCalled();
    expect((fixture.nativeElement as HTMLElement).querySelector('w-detail-fichier')).not.toBeNull();
  });

  it('explique un fichier trop gros par son motif', async () => {
    const refus: Partial<ApiError> = { status: 413, reason: 'file_too_large', message: 'x' };
    const service = {
      arbreDuRun: vi.fn(() => Promise.resolve(ARBRE)),
      fichierDuRun: vi.fn(() => Promise.reject(refus)),
    };
    const fixture = monter(service);
    await fixture.whenStable();
    await cliquer(fixture, '.w-seg button', 'All files');
    await cliquer(fixture, '.w-arbre-ligne', 'site.yml');

    expect((fixture.nativeElement as HTMLElement).textContent).toContain('too large to be shown');
  });

  // Le 501 du diff parle de diffs : pour l'arbre, la phrase doit nommer ce qui manque.
  it('nomme l’absence de lecture du dépôt, pas celle des diffs', async () => {
    const service = { arbreDuRun: vi.fn(() => Promise.reject({ status: 501, message: 'x' })), fichierDuRun: vi.fn() };
    const fixture = monter(service);
    await fixture.whenStable();
    await cliquer(fixture, '.w-seg button', 'All files');

    expect((fixture.nativeElement as HTMLElement).textContent).toContain('Browsing the repository is not available');
  });
});

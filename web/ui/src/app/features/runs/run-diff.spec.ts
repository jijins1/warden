import { Component } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { describe, expect, it, vi } from 'vitest';
import { RunDiff } from './run-diff';
import { DiffsService } from '../../core/api/diffs.service';
import { ApiError } from '../../core/http/api-error';

@Component({ imports: [RunDiff], template: '<w-run-diff runRef="01J" />' })
class Hote {}

async function monter(reponse: () => Promise<string>) {
  const service = { diffDuRun: vi.fn(reponse) };
  TestBed.configureTestingModule({
    providers: [{ provide: DiffsService, useValue: service }],
  });
  const fixture = TestBed.createComponent(Hote);
  await fixture.whenStable();

  const el = fixture.nativeElement as HTMLElement;
  el.querySelector<HTMLButtonElement>('.invite button')?.click();
  await fixture.whenStable();
  return el;
}

const echec = (p: Partial<ApiError>) => () => Promise.reject(p);

describe('RunDiff', () => {
  // Rien n'est calculé d'office : chaque diff coûte un aller au rôle `git`.
  it('ne calcule rien avant qu’on le demande', async () => {
    const service = { diffDuRun: vi.fn(() => Promise.resolve('')) };
    TestBed.configureTestingModule({
      providers: [{ provide: DiffsService, useValue: service }],
    });
    const fixture = TestBed.createComponent(Hote);
    await fixture.whenStable();

    expect(service.diffDuRun).not.toHaveBeenCalled();
  });

  // Une réponse VIDE dit que les deux arbres coïncident. C'est une réponse, pas
  // une donnée manquante — l'afficher comme « rien » serait un contresens.
  it('annonce l’égalité sur un diff vide', async () => {
    const el = await monter(() => Promise.resolve(''));
    expect(el.textContent).toContain('Identical');
  });

  it('classe ajouts, retraits, sections et en-têtes', async () => {
    const el = await monter(() =>
      Promise.resolve(
        ['diff --git a/site.yml b/site.yml', '--- a/site.yml', '+++ b/site.yml',
         '@@ -1,3 +1,4 @@', ' inchangé', '+ajouté', '-retiré'].join('\n'),
      ),
    );

    expect(el.querySelectorAll('.l.ajout')).toHaveLength(1);
    expect(el.querySelectorAll('.l.retrait')).toHaveLength(1);
    expect(el.querySelectorAll('.l.section')).toHaveLength(1);
    // `+++` et `---` sont des en-têtes de fichier : les colorer en vert et rouge
    // ferait lire quatre modifications là où il y en a deux.
    expect(el.querySelectorAll('.l.entete')).toHaveLength(3);
  });

  // Le motif vient de l'en-tête, jamais d'une analyse du corps.
  it('explique une purge par son motif', async () => {
    const el = await monter(echec({ status: 410, reason: 'artifact_pruned', message: 'x' }));
    expect(el.textContent).toContain('pruned');
    expect(el.textContent).toContain('events remain');
  });

  // Le serveur confond délibérément « aucun run antérieur » et « il appartient à
  // quelqu'un d'autre » : le message doit les confondre aussi, sans quoi
  // l'interface rétablirait l'oracle que l'API refuse d'être.
  it('ne distingue pas l’absence de run antérieur de son invisibilité', async () => {
    const el = await monter(echec({ status: 404, reason: 'no_previous_run', message: 'x' }));
    const texte = el.textContent ?? '';
    expect(texte).toContain('No earlier run you can read');
    expect(texte).not.toContain('permission');
    expect(texte).not.toContain('someone else');
  });

  // 501 = déploiement sans client vers le rôle `git`. C'est une configuration
  // valide, pas une panne : le dire comme une panne enverrait chercher un
  // incident qui n'existe pas.
  it('présente l’absence de rôle git comme une configuration, pas une panne', async () => {
    const el = await monter(echec({ status: 501, message: 'x' }));
    expect(el.textContent).toContain('not available in this deployment');
  });

  // Un délai et un échec de calcul appellent des gestes différents — réessayer,
  // ou aller voir le rôle `git`. Deux cas, deux tests : `configureTestingModule`
  // ne se rappelle pas dans un même cas.
  it('nomme un délai du rôle git comme tel', async () => {
    const el = await monter(echec({ status: 504, message: 'x' }));
    expect(el.textContent).toContain('did not answer in time');
  });

  it('nomme un échec de calcul du rôle git comme tel', async () => {
    const el = await monter(echec({ status: 502, message: 'x' }));
    expect(el.textContent).toContain('could not compute');
  });

  // vs=current est la seule des trois comparaisons qui change à chaque appel —
  // elle n'a donc rien à faire si le bouton ne la demande pas vraiment.
  it('demande vs=current au clic sur « vs current playbook »', async () => {
    const service = { diffDuRun: vi.fn(() => Promise.resolve('')) };
    TestBed.configureTestingModule({
      providers: [{ provide: DiffsService, useValue: service }],
    });
    const fixture = TestBed.createComponent(Hote);
    await fixture.whenStable();
    const el = fixture.nativeElement as HTMLElement;

    const bouton = Array.from(el.querySelectorAll<HTMLButtonElement>('.segments button')).find(
      (b) => b.textContent?.includes('vs current playbook'),
    );
    bouton?.click();
    await fixture.whenStable();

    expect(service.diffDuRun).toHaveBeenCalledWith('01J', 'current');
  });
});

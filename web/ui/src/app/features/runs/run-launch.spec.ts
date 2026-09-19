import { Component } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { ActivatedRoute, Router } from '@angular/router';
import { of } from 'rxjs';
import { describe, expect, it, vi } from 'vitest';
import {
  RunLaunch,
  VERBOSITE_MAX,
  ValeursFormulaire,
  composerFlags,
} from './run-launch';
import { RunsService } from '../../core/api/runs.service';

const videForm: ValeursFormulaire = {
  check: false,
  diff: false,
  become: false,
  becomeUser: '',
  limit: '',
  verbosity: 0,
  extraVars: [],
};

describe('composerFlags', () => {
  it('ne produit rien sans option cochée', () => {
    expect(composerFlags(videForm)).toEqual([]);
  });

  it('--check et --diff sont indépendants de --become', () => {
    expect(composerFlags({ ...videForm, check: true, diff: true })).toEqual([
      '--check',
      '--diff',
    ]);
  });

  it('--become-user ne sort que si --become est coché', () => {
    expect(composerFlags({ ...videForm, becomeUser: 'deploy' })).toEqual([]);
    expect(composerFlags({ ...videForm, become: true, becomeUser: 'deploy' })).toEqual([
      '--become',
      '--become-user=deploy',
    ]);
  });

  it('une seule chaîne par --extra-vars, forme nom=valeur', () => {
    expect(
      composerFlags({
        ...videForm,
        extraVars: [
          { cle: 'version', valeur: '1.2.3' },
          { cle: 'debug', valeur: 'true' },
        ],
      }),
    ).toEqual(['--extra-vars=version=1.2.3', '--extra-vars=debug=true']);
  });

  it('une ligne à clé vide est ignorée', () => {
    expect(
      composerFlags({ ...videForm, extraVars: [{ cle: '  ', valeur: 'peu-importe' }] }),
    ).toEqual([]);
  });

  it('--limit est indépendant de --become', () => {
    expect(composerFlags({ ...videForm, limit: 'web*' })).toEqual(['--limit=web*']);
  });

  it('--limit vide ou blanc ne produit rien', () => {
    expect(composerFlags({ ...videForm, limit: '   ' })).toEqual([]);
  });

  // Les `v` se répètent DANS le drapeau : l'allowlist du serveur nomme `-vv`,
  // elle n'accepte pas deux `-v`.
  it('la verbosité sort en un seul drapeau', () => {
    expect(composerFlags({ ...videForm, verbosity: 1 })).toEqual(['-v']);
    expect(composerFlags({ ...videForm, verbosity: 2 })).toEqual(['-vv']);
    expect(composerFlags({ ...videForm, verbosity: VERBOSITE_MAX })).toEqual(['-vvvv']);
  });

  it('une verbosité nulle ne produit rien', () => {
    expect(composerFlags({ ...videForm, verbosity: 0 })).toEqual([]);
  });

  // Le <select> ne propose que 0 à 4, mais `composerFlags` est exportée et
  // testée seule : elle ne doit pas composer un drapeau que le serveur
  // refuserait en 400, ni un `-` nu sur une valeur négative.
  it('la verbosité est bornée des deux côtés', () => {
    expect(composerFlags({ ...videForm, verbosity: 99 })).toEqual(['-vvvv']);
    expect(composerFlags({ ...videForm, verbosity: -3 })).toEqual([]);
    expect(composerFlags({ ...videForm, verbosity: 1.7 })).toEqual(['-v']);
  });
});

@Component({ imports: [RunLaunch], template: '<w-run-launch />' })
class Hote {}

function monter(params: Record<string, string> = {}) {
  const runs = { lancer: vi.fn() };
  const router = { navigate: vi.fn() };
  TestBed.configureTestingModule({
    providers: [
      { provide: RunsService, useValue: runs },
      { provide: Router, useValue: router },
      { provide: ActivatedRoute, useValue: { queryParams: of(params) } },
    ],
  });
  const fixture = TestBed.createComponent(Hote);
  return { runs, router, fixture };
}

describe('RunLaunch', () => {
  it('préremplit le formulaire depuis les paramètres de requête', async () => {
    const { fixture } = monter({ repo: 'depot', environment: 'prod', playbook: 'site.yml' });
    await fixture.whenStable();

    const composant = fixture.debugElement.children[0].componentInstance as RunLaunch;
    const v = composant['formulaire'].getRawValue();
    expect(v.repo).toBe('depot');
    expect(v.environment).toBe('prod');
    expect(v.playbook).toBe('site.yml');
  });

  it('lance le run puis navigue vers son détail', async () => {
    const { runs, router, fixture } = monter({
      repo: 'depot',
      environment: 'prod',
      playbook: 'site.yml',
    });
    runs.lancer.mockResolvedValue({
      run_ref: '01JZQ000000000000000000ABC',
      audit_sha: 'a',
      mainline_ref: 'refs/heads/main',
      mainline_sha: 'a',
    });
    await fixture.whenStable();

    const composant = fixture.debugElement.children[0].componentInstance as RunLaunch;
    await composant['soumettre']();

    expect(runs.lancer).toHaveBeenCalledWith('depot', {
      environment: 'prod',
      inventory: undefined,
      playbook: 'site.yml',
      flags: [],
      become_password: undefined,
    });
    expect(router.navigate).toHaveBeenCalledWith(
      ['/runs', '01JZQ000000000000000000ABC'],
      { replaceUrl: true },
    );
  });

  it('affiche le message du serveur sans naviguer en cas d’échec', async () => {
    const { runs, router, fixture } = monter({
      repo: 'depot',
      environment: 'interdit',
      playbook: 'site.yml',
    });
    runs.lancer.mockRejectedValue({ message: 'you have no access to environment interdit' });
    await fixture.whenStable();

    const composant = fixture.debugElement.children[0].componentInstance as RunLaunch;
    await composant['soumettre']();
    fixture.detectChanges();

    expect(composant['erreur']()).toBe('you have no access to environment interdit');
    expect(router.navigate).not.toHaveBeenCalled();
  });

  it('encode le nom du dépôt dans l’URL', async () => {
    // Vérifié indirectement : le service reçoit le nom TEL QUEL, c'est
    // `RunsService.lancer` (encodeURIComponent) qui porte l'encodage — ce test
    // fige seulement que le composant transmet le nom sans le modifier.
    const { runs, fixture } = monter({
      repo: 'un/depot bizarre',
      environment: 'prod',
      playbook: 'site.yml',
    });
    runs.lancer.mockResolvedValue({
      run_ref: '01JZQ000000000000000000ABC',
      audit_sha: 'a',
      mainline_ref: 'refs/heads/main',
      mainline_sha: 'a',
    });
    await fixture.whenStable();

    const composant = fixture.debugElement.children[0].componentInstance as RunLaunch;
    await composant['soumettre']();

    expect(runs.lancer).toHaveBeenCalledWith('un/depot bizarre', expect.anything());
  });
});

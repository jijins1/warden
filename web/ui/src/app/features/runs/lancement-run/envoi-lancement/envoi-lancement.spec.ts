import { TestBed } from '@angular/core/testing';
import { Router } from '@angular/router';
import { describe, expect, it, vi } from 'vitest';
import { EnvoiLancement } from './envoi-lancement';
import { ValeursLancement } from './formulaire-lancement/formulaire-lancement';
import { RunsService } from '../../../../core/services/runs/runs.service';

const valeurs: ValeursLancement = {
  repo: 'depot',
  environment: 'prod',
  inventory: '',
  playbook: 'site.yml',
  check: false,
  diff: false,
  become: false,
  becomeUser: '',
  becomePassword: '',
  limit: '',
  verbosity: 0,
  extraVars: [],
};

const cree = {
  run_ref: '01JZQ000000000000000000ABC',
  audit_sha: 'a',
  mainline_ref: 'refs/heads/main',
  mainline_sha: 'a',
};

function monter() {
  const runs = { lancer: vi.fn() };
  const router = { navigate: vi.fn() };
  TestBed.configureTestingModule({
    providers: [
      { provide: RunsService, useValue: runs },
      { provide: Router, useValue: router },
    ],
  });
  const fixture = TestBed.createComponent(EnvoiLancement);
  const lancer = (v: ValeursLancement) =>
    (fixture.componentInstance as unknown as { lancer(v: ValeursLancement): Promise<void> }).lancer(v);
  return { runs, router, fixture, lancer };
}

describe('EnvoiLancement', () => {
  it('lance le run puis navigue vers son détail', async () => {
    const { runs, router, lancer } = monter();
    runs.lancer.mockResolvedValue(cree);

    await lancer(valeurs);

    expect(runs.lancer).toHaveBeenCalledWith('depot', {
      environment: 'prod',
      inventory: undefined,
      playbook: 'site.yml',
      flags: [],
      become_password: undefined,
    });
    expect(router.navigate).toHaveBeenCalledWith(['/runs', cree.run_ref], { replaceUrl: true });
  });

  it('compose les drapeaux et transmet le mot de passe become', async () => {
    const { runs, lancer } = monter();
    runs.lancer.mockResolvedValue(cree);

    await lancer({ ...valeurs, become: true, becomeUser: 'root', becomePassword: 's3cret', verbosity: 2 });

    expect(runs.lancer).toHaveBeenCalledWith(
      'depot',
      expect.objectContaining({ flags: ['--become', '--become-user=root', '-vv'], become_password: 's3cret' }),
    );
  });

  it('affiche le message du serveur sans naviguer en cas d’échec', async () => {
    const { runs, router, fixture, lancer } = monter();
    runs.lancer.mockRejectedValue({ message: 'you have no access to environment interdit' });

    await lancer({ ...valeurs, environment: 'interdit' });
    await fixture.whenStable();

    expect((fixture.nativeElement as HTMLElement).querySelector('[role="alert"]')?.textContent).toContain(
      'you have no access to environment interdit',
    );
    expect(router.navigate).not.toHaveBeenCalled();
  });

  it('transmet le nom du dépôt tel quel', async () => {
    // C'est `RunsService.lancer` (encodeURIComponent) qui porte l'encodage — ce
    // test fige seulement que le container transmet le nom sans le modifier.
    const { runs, lancer } = monter();
    runs.lancer.mockResolvedValue(cree);

    await lancer({ ...valeurs, repo: 'un/depot bizarre' });

    expect(runs.lancer).toHaveBeenCalledWith('un/depot bizarre', expect.anything());
  });
});

import { Component } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { describe, expect, it } from 'vitest';
import { StateChip, EtatAffichable } from './state-chip';
import { TerminationCause } from '../../core/models/wire';

@Component({
  imports: [StateChip],
  template: '<w-state-chip [state]="state" [cause]="cause" />',
})
class Hote {
  state: EtatAffichable = 'ok';
  cause: TerminationCause | '' | undefined = undefined;
}

async function rendre(state: EtatAffichable, cause?: TerminationCause | '') {
  const fixture = TestBed.createComponent(Hote);
  fixture.componentInstance.state = state;
  fixture.componentInstance.cause = cause;
  await fixture.whenStable();
  return fixture.nativeElement as HTMLElement;
}

describe('StateChip', () => {
  // Règle 1 : icône, mot ET couleur, toujours les trois. La couleur seule échoue
  // pour un lecteur daltonien et échoue encore à faible contraste.
  it('porte toujours une icône et un mot, pas seulement une couleur', async () => {
    for (const etat of ['ok', 'changed', 'failed', 'unreachable', 'skipped',
                        'unknown', 'running', 'succeeded'] as EtatAffichable[]) {
      const el = await rendre(etat);
      expect(el.querySelector('svg'), `${etat} sans icône`).not.toBeNull();
      expect(el.querySelector('.w-chip-mot')?.textContent?.trim(), `${etat} sans mot`).toBeTruthy();
      expect(el.querySelector('.w-chip')?.getAttribute('data-state')).toBeTruthy();
    }
  });

  // `succeeded` (run) et `ok` (event) partagent le vert : deux vocabulaires,
  // une seule signification.
  it('mappe succeeded sur le jeton ok', async () => {
    const el = await rendre('succeeded');
    expect(el.querySelector('.w-chip')?.getAttribute('data-state')).toBe('ok');
  });

  // Règle 3 : `interrupted` n'est PAS un échec. L'ADR-0031 pose qu'un runner
  // mourant ne prononce pas de verdict.
  it('ne peint pas interrupted comme un échec', async () => {
    const el = await rendre('interrupted', 'pod_evicted');
    expect(el.querySelector('.w-chip')?.getAttribute('data-state')).toBe('interrupted');
    expect(el.querySelector('.w-chip')?.getAttribute('data-state')).not.toBe('failed');
  });

  // Règle 2, et LE test de ce fichier. « Interrompu » sans la cause est presque
  // inutile : un Ctrl-C est un signal fort, un client_disconnect_timeout est
  // accidentel. Les confondre jette l'information qui compte.
  it('affiche toujours la cause avec interrupted', async () => {
    const el = await rendre('interrupted', 'user_interrupt');
    expect(el.querySelector('.w-cause')?.textContent?.trim()).toBe('user_interrupt');
  });

  // Le schéma garantit `interrupted` ⟺ cause renseignée. Une cause absente est
  // donc un défaut du CLIENT : il est rendu visible plutôt que masqué, parce
  // qu'un chip muet ferait exactement le tort que la règle interdit.
  it('rend visible une cause manquante au lieu de la taire', async () => {
    const el = await rendre('interrupted', '');
    const cause = el.querySelector('.w-cause');
    expect(cause).not.toBeNull();
    expect(cause?.textContent?.trim()).toBe('cause missing');
  });

  it('distingue user_interrupt comme signal fort', async () => {
    const fort = await rendre('interrupted', 'user_interrupt');
    expect(fort.querySelector('.w-cause')?.classList.contains('is-strong')).toBe(true);

    const accidentel = await rendre('interrupted', 'client_disconnect_timeout');
    expect(accidentel.querySelector('.w-cause')?.classList.contains('is-strong')).toBe(false);
  });

  // Aucun autre état ne porte de cause : le domaine l'interdit
  // (`Closure.Validate` refuse une cause sur un état non interrompu).
  it("n'affiche pas de cause sur les autres états", async () => {
    for (const etat of ['succeeded', 'failed', 'running'] as EtatAffichable[]) {
      const el = await rendre(etat, 'user_interrupt');
      expect(el.querySelector('.w-cause'), `${etat} porte une cause`).toBeNull();
    }
  });
});

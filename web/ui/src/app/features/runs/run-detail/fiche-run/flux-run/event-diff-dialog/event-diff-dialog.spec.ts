import { TestBed } from '@angular/core/testing';
import { describe, expect, it, vi } from 'vitest';
import { MAT_DIALOG_DATA } from '@angular/material/dialog';
import { EventDiffDialog } from './event-diff-dialog';
import { DiffsService } from '../../../../../../core/services/diffs/diffs.service';

async function monter(reponse: () => Promise<unknown>) {
  TestBed.configureTestingModule({
    providers: [
      { provide: DiffsService, useValue: { diffDEvent: vi.fn(reponse) } },
      { provide: MAT_DIALOG_DATA, useValue: { runRef: '01J', seq: 7, etat: 'changed' } },
    ],
  });
  const fixture = TestBed.createComponent(EventDiffDialog);
  await fixture.whenStable();
  return fixture.nativeElement as HTMLElement;
}

describe('EventDiffDialog', () => {
  // `null` est une réponse : la tâche n'a produit aucun diff. Ni une erreur, ni
  // une absence de droit.
  it('dit qu’une tâche sans diff n’en a pas produit', async () => {
    const el = await monter(() => Promise.resolve(null));
    expect(el.querySelector('.w-nothing')?.textContent).toContain('produced no diff');
    expect(el.querySelector('[role="alert"]')).toBeNull();
  });

  it('affiche un diff structuré en texte', async () => {
    const el = await monter(() => Promise.resolve({ before: 'a', after: 'b' }));
    expect(el.querySelector('.w-diff')?.textContent).toContain('"after": "b"');
  });
});

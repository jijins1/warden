import { TestBed } from '@angular/core/testing';
import { describe, expect, it } from 'vitest';
import { Auteur } from './auteur';

function rendre(subject: string, displayName?: string): string {
  const fixture = TestBed.createComponent(Auteur);
  fixture.componentRef.setInput('subject', subject);
  if (displayName !== undefined) {
    fixture.componentRef.setInput('displayName', displayName);
  }
  fixture.detectChanges();
  return ((fixture.nativeElement as HTMLElement).textContent ?? '').trim();
}

describe('Auteur', () => {
  it('affiche le nom quand le compte en a un', () => {
    expect(rendre('mallory', 'Mallory M.')).toBe('Mallory M.');
  });

  // Un compte sans nom — OIDC sans claim, compte local créé sans — ne doit
  // pas laisser la cellule vide : le sujet est toujours là.
  it('retombe sur le sujet sans nom', () => {
    expect(rendre('mallory', '')).toBe('mallory');
    expect(rendre('mallory')).toBe('mallory');
  });
});

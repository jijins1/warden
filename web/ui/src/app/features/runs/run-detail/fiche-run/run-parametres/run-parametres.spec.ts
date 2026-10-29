import { TestBed } from '@angular/core/testing';
import { describe, expect, it } from 'vitest';
import { RunParametres } from './run-parametres';

describe('RunParametres', () => {
  it('ne dit pas « sans drapeau » quand le run n’avait que des tags', async () => {
    const fixture = TestBed.createComponent(RunParametres);
    fixture.componentRef.setInput('flags', ['--tags=deploy']);
    await fixture.whenStable();

    const texte = (fixture.nativeElement as HTMLElement).textContent ?? '';
    expect(texte).not.toContain('Ran without flags');
    expect(texte).toContain('No other flags');
  });

  it('affiche les extra vars en clair, nom et valeur', async () => {
    const fixture = TestBed.createComponent(RunParametres);
    fixture.componentRef.setInput('flags', ['--diff', '--extra-vars=version=1.2']);
    await fixture.whenStable();

    const texte = (fixture.nativeElement as HTMLElement).textContent ?? '';
    expect(texte).toContain('--diff');
    expect(texte).toContain('version');
    expect(texte).toContain('1.2');
  });

  it('dit qu’un run sans drapeau n’en avait pas', async () => {
    const fixture = TestBed.createComponent(RunParametres);
    await fixture.whenStable();

    expect((fixture.nativeElement as HTMLElement).textContent).toContain('Ran without flags');
  });
});

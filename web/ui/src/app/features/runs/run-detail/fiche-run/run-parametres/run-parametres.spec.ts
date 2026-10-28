import { TestBed } from '@angular/core/testing';
import { describe, expect, it } from 'vitest';
import { RunParametres, lireFlags } from './run-parametres';

describe('lireFlags', () => {
  it('sépare les options des extra vars, dans l’ordre reçu', () => {
    expect(lireFlags(['--check', '--limit=web*', '--extra-vars=version=1.2', '--extra-vars=b=2'])).toEqual({
      options: ['--check', '--limit=web*'],
      extraVars: [
        { nom: 'version', valeur: '1.2' },
        { nom: 'b', valeur: '2' },
      ],
    });
  });

  // Le nom s'arrête au PREMIER `=` : une URL ou une chaîne de connexion en
  // valeur en contient d'autres, et les couper au dernier renommerait la variable.
  it('garde les `=` de la valeur', () => {
    expect(lireFlags(['--extra-vars=url=https://x?a=b']).extraVars).toEqual([
      { nom: 'url', valeur: 'https://x?a=b' },
    ]);
  });
});

describe('RunParametres', () => {
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

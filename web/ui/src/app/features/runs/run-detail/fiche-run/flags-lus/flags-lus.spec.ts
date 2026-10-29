import { describe, expect, it } from 'vitest';
import { avecTags, lireFlags } from './flags-lus';

describe('lireFlags', () => {
  it('sépare les options des extra vars, dans l’ordre reçu', () => {
    expect(
      lireFlags(['--check', '--limit=web*', '--extra-vars=version=1.2', '--extra-vars=b=2']),
    ).toEqual({
      options: ['--check', '--limit=web*'],
      extraVars: [
        { nom: 'version', valeur: '1.2' },
        { nom: 'b', valeur: '2' },
      ],
      tags: [],
      skipTags: [],
    });
  });

  // Le nom s'arrête au PREMIER `=` : une URL ou une chaîne de connexion en
  // valeur en contient d'autres, et les couper au dernier renommerait la variable.
  it('garde les `=` de la valeur', () => {
    expect(lireFlags(['--extra-vars=url=https://x?a=b']).extraVars).toEqual([
      { nom: 'url', valeur: 'https://x?a=b' },
    ]);
  });

  // Les tags sortent des options : la fiche les montre à part, et les laisser
  // aussi dans la rangée les afficherait deux fois.
  it('sort les tags des options, un nom par tag', () => {
    const lus = lireFlags(['--check', '--tags=deploy,config', '--skip-tags=slow']);
    expect(lus.options).toEqual(['--check']);
    expect(lus.tags).toEqual(['deploy', 'config']);
    expect(lus.skipTags).toEqual(['slow']);
  });
});

describe('avecTags', () => {
  it('vrai dès qu’une des deux listes est posée', () => {
    expect(avecTags(lireFlags(['--check']))).toBe(false);
    expect(avecTags(lireFlags(['--skip-tags=slow']))).toBe(true);
  });
});

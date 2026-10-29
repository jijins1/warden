import { describe, expect, it } from 'vitest';
import { OptionsLancement, VERBOSITE_MAX, composerFlags } from './flags';

const videForm: OptionsLancement = {
  check: false,
  diff: false,
  become: false,
  becomeUser: '',
  limit: '',
  tags: '',
  skipTags: '',
  verbosity: 0,
  extraVars: [],
};

describe('composerFlags', () => {
  it('--tags et --skip-tags, une chaîne chacun, sans les espaces autour des virgules', () => {
    expect(composerFlags({ ...videForm, tags: ' deploy, config ,', skipTags: 'slow' })).toEqual([
      '--tags=deploy,config',
      '--skip-tags=slow',
    ]);
  });

  it('des tags blancs ne produisent rien', () => {
    expect(composerFlags({ ...videForm, tags: ' , ', skipTags: '  ' })).toEqual([]);
  });

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

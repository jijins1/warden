import { describe, expect, it } from 'vitest';
import { colorer, grammairePour } from './coloration';

const roles = (texte: string, chemin: string) =>
  colorer(texte, grammairePour(chemin))
    .filter((f) => f.texte.trim())
    .map((f) => [f.texte, f.genre]);

describe('colorer', () => {
  it('colore une tâche Ansible, Jinja compris à l’intérieur des chaînes', () => {
    expect(roles('    src: "{{ item }}.conf.j2"', 'roles/web/tasks/main.yml')).toEqual([
      ['src', 'cle'],
      [':', 'ponct'],
      ['"', 'chaine'],
      ['{{', 'gabarit'],
      ['item', 'gabarit'],
      ['}}', 'gabarit'],
      ['.conf.j2"', 'chaine'],
    ]);
  });

  it('reconnaît un fichier de variables sans extension', () => {
    expect(roles('enabled: true', 'group_vars/prod')).toContainEqual(['true', 'valeur']);
  });

  // Un gabarit dont le langage rendu est inconnu garde au moins son Jinja.
  it('colore le Jinja d’un gabarit de langage inconnu', () => {
    expect(roles('Hello {{ user }}', 'templates/motd.j2')).toEqual([
      ['Hello ', null],
      ['{{', 'gabarit'],
      ['user', 'gabarit'],
      ['}}', 'gabarit'],
    ]);
  });

  it('laisse brut un fichier de langage inconnu', () => {
    expect(colorer('a: "b"', grammairePour('README'))).toEqual([{ texte: 'a: "b"', genre: null }]);
  });
});

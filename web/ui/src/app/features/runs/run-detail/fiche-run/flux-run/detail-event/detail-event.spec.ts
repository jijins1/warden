import { describe, expect, it } from 'vitest';
import { detailDe, moduleCourt } from './detail-event';

const tache = (res: Record<string, unknown>, extra: Record<string, unknown> = {}) => ({
  stdout: 'ok: [cible-test]',
  event_data: { task_action: 'ansible.builtin.command', res, ...extra },
});

describe('detailDe', () => {
  // LE point : le `stdout` de premier niveau vaut « ok: [cible-test] » et ne dit
  // rien. Ce que la tâche a réellement fait est dans `event_data.res`.
  it('rend la commande lancée et sa vraie sortie', () => {
    const d = detailDe(tache({ cmd: ['id', '-un'], stdout: 'ansible', rc: 0 }));
    expect(d.module).toBe('ansible.builtin.command');
    expect(d.commande).toBe('id -un');
    expect(d.sortie).toBe('ansible');
    expect(d.code).toBe(0);
  });

  // `cmd` est parfois déjà une chaîne selon le module.
  it('accepte une commande déjà en chaîne', () => {
    expect(detailDe(tache({ cmd: 'ls -l' })).commande).toBe('ls -l');
  });

  // Une tâche marquée `no_log` est censurée par Ansible LUI-MÊME. L'afficher
  // quand même contredirait une décision prise dans le playbook, qui est le seul
  // endroit sachant pourquoi cette tâche est sensible.
  it('respecte no_log', () => {
    const d = detailDe(tache({ cmd: ['secret'], stdout: 'x' }, {}));
    expect(d.censure).toBe(false);

    const c = detailDe({
      event_data: { res: { _ansible_no_log: true, stdout: 'x', cmd: ['secret'] } },
    });
    expect(c.censure).toBe(true);
    expect(c.sortie).toBe('');
    expect(c.commande).toBe('');
  });

  // Sans `res` — les events de cycle de vie — on retombe sur le stdout rendu par
  // ansible-runner, qui est alors la seule chose disponible.
  it('retombe sur le stdout de premier niveau', () => {
    const d = detailDe({ stdout: '\r\nPLAY [essai] ***' });
    expect(d.sortie).toBe('PLAY [essai] ***');
    expect(d.module).toBe('');
  });

  // Le runner force la couleur d'Ansible pour la CLI : le texte rendu porte des
  // séquences d'échappement, qu'un <pre> afficherait en clair (« [0;32m »).
  it('retire les couleurs du texte rendu par Ansible', () => {
    const d = detailDe({ stdout: '\u001b[0;32mok: [kodi]\u001b[0m' });
    expect(d.sortie).toBe('ok: [kodi]');
  });

  // Le payload vient d'un module Ansible, donc de la machine gérée : sa forme
  // n'est pas garantie. Rien ne doit lever.
  it('ne lève sur aucune forme inattendue', () => {
    for (const cas of [null, undefined, 42, 'brut', {}, { event_data: 7 }, { event_data: { res: 3 } }]) {
      expect(() => detailDe(cas)).not.toThrow();
      expect(detailDe(cas).sortie).toBe('');
    }
  });
});

describe('moduleCourt', () => {
  // Dans la LISTE, le préfixe de collection mange la place utile et se répète
  // sur chaque ligne. C'est le nom du module qui distingue une tâche d'une autre.
  it('retire le préfixe de collection', () => {
    expect(moduleCourt('ansible.builtin.command')).toBe('command');
    expect(moduleCourt('community.general.ufw')).toBe('ufw');
    expect(moduleCourt('copy')).toBe('copy');
    expect(moduleCourt('')).toBe('');
  });
});

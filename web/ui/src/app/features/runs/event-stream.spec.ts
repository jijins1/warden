import { describe, expect, it } from 'vitest';
import { aplatir, detailDe, GroupeTache, moduleCourt } from './event-stream';
import { LigneFlux } from '../../core/api/run-stream';
import { EventFrame } from '../../core/api/wire-ws';
import { EventState } from '../../core/api/wire';

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

  // Le payload vient d'un module Ansible, donc de la machine gérée : sa forme
  // n'est pas garantie. Rien ne doit lever.
  it('ne lève sur aucune forme inattendue', () => {
    for (const cas of [null, undefined, 42, 'brut', {}, { event_data: 7 }, { event_data: { res: 3 } }]) {
      expect(() => detailDe(cas)).not.toThrow();
      expect(detailDe(cas).sortie).toBe('');
    }
  });
});

const event = (
  seq: number,
  taskNumber: number,
  host: string,
  state: EventState,
  extra: Partial<EventFrame> = {},
): LigneFlux => ({
  kind: 'event',
  event: {
    seq,
    occurred_at: '2026-01-01T00:00:00Z',
    task_number: taskNumber,
    task_name: 'installer nginx',
    role: 'nginx',
    host,
    state,
    payload: {},
    ...extra,
  },
});

const jamaisDeplie = () => false;
const toujoursDeplie = () => true;

/** Le diagnostic d'un run qui n'a rien exécuté : sans hôte, sans tâche. */
const diagnostic = (seq: number, message: string): LigneFlux =>
  event(seq, 0, '', 'failed', {
    task_name: '',
    role: '',
    payload: { warden_error: message },
  });

describe('aplatir', () => {
  // LE point du regroupement : la question posée sur une flotte est « combien
  // ont changé sur cette tâche », pas le détail machine par machine.
  it('regroupe les events par task_number en une ligne tache', () => {
    const lignes = [
      event(0, 1, 'kodi', 'ok'),
      event(1, 1, 'muzi', 'changed'),
      event(2, 2, 'kodi', 'ok'),
    ];

    const plat = aplatir(lignes, jamaisDeplie);

    expect(plat.map((l) => l.kind)).toEqual(['tache', 'tache']);
    const [t1, t2] = plat as { kind: 'tache'; groupe: GroupeTache }[];
    expect(t1.groupe.comptes).toEqual({ ok: 1, changed: 1 });
    expect(t2.groupe.comptes).toEqual({ ok: 1 });
  });

  // Un event sans hôte vient du play lui-même, pas d'une machine : c'est le
  // bruit qui noyait les tâches sur un run bavard, et il ne doit produire
  // AUCUNE ligne, ni tâche ni event.
  it('écarte les events sans hôte', () => {
    const lignes = [event(0, 1, '', 'ok'), event(1, 1, 'kodi', 'ok')];

    const plat = aplatir(lignes, toujoursDeplie);

    expect(plat).toHaveLength(2); // 1 tache + 1 event, pas 2+2
  });

  // `runner_on_start` annonce qu'une tâche démarre sur un hôte donné, AVANT
  // tout résultat — il porte un hôte, contrairement au bruit sans hôte, mais
  // ce n'est toujours pas un résultat. Le compter produirait un « unknown=1 »
  // sur CHAQUE tâche.
  it('écarte les events « unknown » même avec un hôte', () => {
    const lignes = [event(0, 1, 'kodi', 'unknown'), event(1, 1, 'kodi', 'changed')];

    const plat = aplatir(lignes, toujoursDeplie) as { kind: string; groupe?: GroupeTache }[];

    expect(plat.map((l) => l.kind)).toEqual(['tache', 'event']);
    expect(plat[0].groupe?.comptes).toEqual({ changed: 1 });
  });

  // Un run qui échoue sans qu'AUCUNE tâche ne démarre ne produit que des events
  // sans hôte — tous écartés par le filtre ci-dessus, ce qui laissait la page
  // muette sur un `failed`. Le diagnostic écrit par warden doit passer.
  //
  // Ce test échouerait si le filtre se remettait à l'avaler : c'est sa raison
  // d'être.
  it('garde le diagnostic d’un run qui n’a rien exécuté', () => {
    const plat = aplatir([diagnostic(0, 'playbook introuvable : site.yml')], jamaisDeplie);

    expect(plat.map((l) => l.kind)).toEqual(['erreur']);
    expect(plat[0]).toMatchObject({ message: 'playbook introuvable : site.yml' });
  });

  // …mais LUI SEUL. Un run qui RÉUSSIT émet déjà des events `verbose` sans hôte
  // — mesuré : cinq, rien que pour l'avertissement d'interpréteur Python. Les
  // laisser passer noierait chaque run sous du bruit.
  it('écarte toujours les events sans hôte qui ne portent pas de diagnostic', () => {
    const lignes = [
      event(0, 0, '', 'unknown', { payload: { stdout: '[WARNING]: Platform linux…' } }),
      event(1, 1, 'kodi', 'changed'),
    ];

    const plat = aplatir(lignes, jamaisDeplie);

    expect(plat.map((l) => l.kind)).toEqual(['tache']);
  });

  // Une marque de reprise garde sa place chronologique et n'appartient à
  // aucune tâche : la dépliée suivante commence un nouveau groupe.
  it('garde les marques de reprise à plat et coupe le groupe en cours', () => {
    const lignes: LigneFlux[] = [
      event(0, 1, 'kodi', 'ok'),
      { kind: 'resume', seq: 5 },
      event(1, 1, 'kodi', 'ok'),
    ];

    const plat = aplatir(lignes, jamaisDeplie);

    expect(plat.map((l) => l.kind)).toEqual(['tache', 'resume', 'tache']);
  });

  // Dépliée, une tâche insère ses events juste après sa ligne d'en-tête, DANS
  // LA MÊME liste à plat — c'est ce qui la rend compatible avec le défilement
  // virtuel, qui a besoin d'un flux plat plutôt que d'un résumé imbriquant
  // son détail.
  it("insère les events d'une tâche dépliée juste après son en-tête", () => {
    const lignes = [event(0, 1, 'kodi', 'failed'), event(1, 1, 'muzi', 'ok')];

    const plat = aplatir(lignes, toujoursDeplie);

    expect(plat.map((l) => l.kind)).toEqual(['tache', 'event', 'event']);
  });

  // `failed`/`unreachable` restent nommées à part : jamais repliées dans un
  // simple compte, contrairement aux autres états.
  it('retient les hôtes en échec séparément du compte', () => {
    const lignes = [
      event(0, 1, 'kodi', 'failed'),
      event(1, 1, 'muzi', 'unreachable'),
      event(2, 1, 'zima', 'ok'),
    ];

    const [tache] = aplatir(lignes, jamaisDeplie) as { kind: 'tache'; groupe: GroupeTache }[];

    expect(tache.groupe.echecs.map((e) => e.host)).toEqual(['kodi', 'muzi']);
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

/** Ce qu'une tâche a réellement fait, extrait du payload d'un event. */
export interface DetailEvent {
  /** Le module invoqué — `ansible.builtin.copy`. Vide sur un event de cycle de vie. */
  readonly module: string;
  /** La commande réellement lancée, quand le module en lance une. */
  readonly commande: string;
  /** Le code de retour, quand le module en rend un. */
  readonly code: number | null;
  readonly sortie: string;
  readonly erreur: string;
  readonly message: string;
  /** La tâche est marquée `no_log` : Ansible l'a censurée, et nous aussi. */
  readonly censure: boolean;
}

const VIDE: DetailEvent = {
  module: '',
  commande: '',
  code: null,
  sortie: '',
  erreur: '',
  message: '',
  censure: false,
};

/**
 * Les séquences de couleur qu'Ansible écrit (`ESC [ … m`).
 *
 * Le runner force la couleur d'Ansible pour que la CLI la restitue ; un `<pre>`
 * l'afficherait en clair. Construite par `RegExp` pour ne pas porter de
 * caractère de contrôle littéral dans le source.
 */
const SEQUENCE_SGR = new RegExp(String.fromCharCode(27) + '\\[[0-9;]*m', 'g');

function chaine(v: unknown): string {
  // Les sauts de ligne DE TÊTE seulement : `ansible-runner` préfixe ses lignes
  // par `\r\n`, et un `trim()` complet mangerait l'indentation d'une sortie
  // structurée — ce qui la rendrait illisible là où elle compte.
  return typeof v === 'string'
    ? v.replace(SEQUENCE_SGR, '').replace(/^[\r\n]+/, '').trimEnd()
    : '';
}

function objet(v: unknown): Record<string, unknown> | null {
  return v && typeof v === 'object' ? (v as Record<string, unknown>) : null;
}

/**
 * Ce que la tâche a fait, et pas seulement comment elle s'appelle.
 *
 * LE point de cette fonction : le `stdout` de premier niveau d'un résultat de
 * tâche vaut « ok: [machine] » — il ne dit rien. Ce qui compte est dans
 * `event_data.res` : la commande lancée, son code de retour, sa sortie réelle.
 *
 * Tout est déjà transmis par le flux et déjà EXPURGÉ : le rapporteur du runner
 * masque le payload entier avant qu'il quitte le pod (ADR-0017) et l'abandonne
 * si la rédaction est invérifiable. Lire ces champs n'élargit pas la surface
 * d'audit — contrairement au payload `--diff`, qui garde son contrôle d'accès.
 *
 * `_ansible_no_log` est RESPECTÉ. Une tâche marquée `no_log` a été censurée par
 * Ansible lui-même, sur une décision prise dans le playbook — le seul endroit
 * qui sache pourquoi elle est sensible. La contredire ici serait rendre visible
 * ce que son auteur a demandé de taire.
 */
export function detailDe(payload: unknown): DetailEvent {
  const p = objet(payload);
  if (!p) {
    return VIDE;
  }

  const res = objet(objet(p['event_data'])?.['res']);
  const module = chaine(objet(p['event_data'])?.['task_action']);

  if (res?.['_ansible_no_log'] === true) {
    return { ...VIDE, module, censure: true };
  }

  const cmd = res?.['cmd'];
  const commande = Array.isArray(cmd)
    ? cmd.map((m) => String(m)).join(' ')
    : chaine(cmd);

  const rc = res?.['rc'];

  return {
    module,
    commande,
    code: typeof rc === 'number' ? rc : null,
    // `res.stdout` d'abord : le stdout de premier niveau n'est que la ligne
    // rendue par ansible-runner. Il sert de repli pour les events de cycle de
    // vie, où il est la seule chose disponible.
    sortie: chaine(res?.['stdout']) || chaine(p['stdout']),
    erreur: chaine(res?.['stderr']),
    message: chaine(res?.['msg']),
    censure: false,
  };
}

/**
 * Le nom du module sans son préfixe de collection.
 *
 * Dans la LISTE seulement : `ansible.builtin.` se répète sur chaque ligne et
 * mange la place utile, alors que c'est le nom du module qui distingue une
 * tâche d'une autre. Le dialogue, lui, montre le nom complet.
 */
export function moduleCourt(module: string): string {
  const i = module.lastIndexOf('.');
  return i === -1 ? module : module.slice(i + 1);
}

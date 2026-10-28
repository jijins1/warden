import { LigneFlux } from '../../../../../../../core/services/run-stream/run-stream';
import { EventState } from '../../../../../../../core/models/wire';
import { EventFrame } from '../../../../../../../core/models/wire-ws';

/**
 * Le résumé d'une tâche : une ligne d'en-tête repliable, plutôt qu'une ligne
 * par event.
 *
 * `cle` est le `task_number` du serveur — stable sur toute la durée du run,
 * ce qui permet à `estDeplie()` de retenir un dépli au fil des arrivées
 * d'events sans perdre l'état à chaque recalcul.
 */
export interface GroupeTache {
  readonly cle: string;
  readonly taskName: string;
  readonly role: string;
  readonly events: readonly EventFrame[];
  /** Compte par état, POUR CETTE TÂCHE seule — pas le total du run. */
  readonly comptes: Readonly<Partial<Record<EventState, number>>>;
  /** `failed`/`unreachable` seulement : jamais repliés dans un simple compte. */
  readonly echecs: readonly EventFrame[];
}

/**
 * Une ligne de la liste affichée — À PLAT, jamais imbriquée.
 *
 * Une tâche repliée n'apparaît que par sa ligne `tache` ; en la dépliant, ses
 * events s'insèrent juste après dans la MÊME liste. C'est délibéré : la liste
 * reste virtualisée par `cdk-virtual-scroll`, qui a besoin d'un flux plat
 * d'éléments de hauteur connue — une structure imbriquée (résumé contenant
 * son détail) casserait cette hypothèse dès qu'une tâche est ouverte.
 */
export type LigneGroupee =
  | { readonly kind: 'resume'; readonly seq: number }
  | { readonly kind: 'erreur'; readonly seq: number; readonly message: string }
  | { readonly kind: 'tache'; readonly groupe: GroupeTache; readonly deplie: boolean }
  | { readonly kind: 'event'; readonly event: EventFrame };

/**
 * `events.CleErreur` — internal/events/diagnostic.go.
 *
 * Miroir manuel d'une constante Go, comme les types de `wire.ts` : c'est la clé
 * qu'un payload porte quand le message a été écrit par WARDEN et non recopié
 * d'`ansible-runner`.
 */
const CLE_ERREUR = 'warden_error';

/**
 * Le message de diagnostic d'un event, vide s'il n'en porte pas.
 *
 * Ce discriminant est ce qui permet d'afficher CE message sans rouvrir la porte
 * au bruit : un run qui RÉUSSIT émet déjà des events `verbose` sans hôte —
 * mesuré : cinq, rien que pour l'avertissement de découverte d'interpréteur
 * Python — et c'est précisément ce que le filtre ci-dessous écarte.
 *
 * Tolérante : un payload d'une autre forme rend une chaîne vide. Elle est
 * appelée sur chaque event, y compris ceux d'un pod hostile.
 */
function diagnostic(e: EventFrame): string {
  const p = e.payload as Record<string, unknown> | null | undefined;
  if (!p || typeof p !== 'object') {
    return '';
  }
  const msg = p[CLE_ERREUR];
  return typeof msg === 'string' ? msg : '';
}

/**
 * Regroupe le flux par tâche, à plat.
 *
 * Une ligne par event noie la question qu'on pose sur une flotte — « combien
 * ont changé sur cette tâche » — sous des dizaines de lignes identiques. Le
 * regroupement suit `task_number`, le même identifiant que `warden logs`
 * utilise déjà côté CLI pour la même raison.
 */
function grouperParTache(
  lignes: readonly LigneFlux[],
): (GroupeTache | { seq: number } | { seq: number; erreur: string })[] {
  const resultat: (GroupeTache | { seq: number } | { seq: number; erreur: string })[] = [];
  let courant: {
    cle: string;
    taskName: string;
    role: string;
    events: EventFrame[];
    comptes: Partial<Record<EventState, number>>;
    echecs: EventFrame[];
  } | null = null;

  for (const ligne of lignes) {
    if (ligne.kind === 'resume') {
      courant = null;
      resultat.push({ seq: ligne.seq });
      continue;
    }

    const e = ligne.event;

    // Le DIAGNOSTIC passe AVANT le filtre, et c'est tout son objet : un run qui
    // échoue sans qu'aucune tâche ne démarre — playbook introuvable, erreur de
    // syntaxe — ne produit que des events sans hôte. Ils étaient donc tous
    // écartés, et la page restait muette sur un run `failed`.
    //
    // Il ne rejoint aucun groupe : il n'appartient à aucune tâche, puisqu'il
    // n'y en a pas eu. Comme une marque de reprise, il coupe le groupe courant.
    const message = diagnostic(e);
    if (message) {
      courant = null;
      resultat.push({ seq: e.seq, erreur: message });
      continue;
    }

    // Un event SANS hôte vient du play lui-même, pas d'une machine : rien à
    // compter par tâche, et l'afficher ne serait que du bruit — une ligne
    // vide répétée à chaque event de cycle de vie d'Ansible.
    //
    // `unknown` AVEC hôte existe aussi — `runner_on_start` annonce qu'une
    // tâche démarre sur cette machine, avant tout résultat. Le compter
    // produirait un « unknown=1 » sur CHAQUE tâche, pour une annonce et non
    // un résultat : du bruit garanti plutôt qu'occasionnel.
    if (!e.host || e.state === 'unknown') {
      continue;
    }

    const cle = String(e.task_number ?? 0);
    if (!courant || courant.cle !== cle) {
      courant = {
        cle,
        taskName: e.task_name ?? '',
        role: e.role ?? '',
        events: [],
        comptes: {},
        echecs: [],
      };
      resultat.push(courant);
    }

    courant.events.push(e);
    courant.comptes[e.state] = (courant.comptes[e.state] ?? 0) + 1;
    if (e.state === 'failed' || e.state === 'unreachable') {
      courant.echecs.push(e);
    }
  }

  return resultat;
}

/**
 * Aplatit les groupes en lignes affichables : l'en-tête de chaque tâche,
 * suivi de ses events SI ET SEULEMENT SI `deplie` la dit ouverte.
 */
export function aplatir(
  lignes: readonly LigneFlux[],
  deplie: (g: GroupeTache) => boolean,
): LigneGroupee[] {
  const resultat: LigneGroupee[] = [];
  for (const l of grouperParTache(lignes)) {
    if ('erreur' in l) {
      resultat.push({ kind: 'erreur', seq: l.seq, message: l.erreur });
      continue;
    }
    if (!('cle' in l)) {
      resultat.push({ kind: 'resume', seq: l.seq });
      continue;
    }
    const ouvert = deplie(l);
    resultat.push({ kind: 'tache', groupe: l, deplie: ouvert });
    if (ouvert) {
      for (const e of l.events) {
        resultat.push({ kind: 'event', event: e });
      }
    }
  }
  return resultat;
}

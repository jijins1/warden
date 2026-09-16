import {
  ChangeDetectionStrategy,
  Component,
  computed,
  effect,
  inject,
  input,
  signal,
  viewChild,
} from '@angular/core';
import { DatePipe } from '@angular/common';
import { CdkVirtualScrollViewport, ScrollingModule } from '@angular/cdk/scrolling';
import { MatDialog } from '@angular/material/dialog';
import { MatIconModule } from '@angular/material/icon';
import { MatTooltipModule } from '@angular/material/tooltip';
import { LigneFlux, RunStream } from '../../core/api/run-stream';
import { EventState } from '../../core/api/wire';
import { StateChip } from '../../shared/state-chip/state-chip';
import { EventDiffDialog } from './event-diff-dialog';
import { EventFrame } from '../../core/api/wire-ws';

/** Filtres d'affichage. Purement locaux : le flux, lui, reçoit tout. */
type Filtre = 'all' | 'results' | 'changed' | 'failed' | 'unreachable';

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
  | { readonly kind: 'tache'; readonly groupe: GroupeTache }
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
    resultat.push({ kind: 'tache', groupe: l });
    if (deplie(l)) {
      for (const e of l.events) {
        resultat.push({ kind: 'event', event: e });
      }
    }
  }
  return resultat;
}

/** Fige l'ordre des compteurs dans le résumé d'une tâche : `changed` en tête,
 * c'est la métrique d'audit principale du produit (ADR-0008). */
const ORDRE_COMPTES: EventState[] = ['changed', 'ok', 'failed', 'unreachable', 'skipped'];

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

function chaine(v: unknown): string {
  // Les sauts de ligne DE TÊTE seulement : `ansible-runner` préfixe ses lignes
  // par `\r\n`, et un `trim()` complet mangerait l'indentation d'une sortie
  // structurée — ce qui la rendrait illisible là où elle compte.
  return typeof v === 'string' ? v.replace(/^[\r\n]+/, '').trimEnd() : '';
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


@Component({
  selector: 'w-event-stream',
  imports: [DatePipe, ScrollingModule, MatIconModule, MatTooltipModule, StateChip],
  templateUrl: './event-stream.html',
  styleUrl: './event-stream.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class EventStream {
  readonly flux = input.required<RunStream>();
  /** La référence du run, pour aller chercher le diff d'un event. */
  readonly runRef = input.required<string>();

  readonly #dialogue = inject(MatDialog);

  protected readonly filtre = signal<Filtre>('all');
  protected readonly filtres: Filtre[] = ['all', 'results', 'changed', 'failed', 'unreachable'];

  /**
   * Suivre le bas du flux, sauf si l'utilisateur est remonté.
   *
   * Sans ce verrou, lire un event ancien deviendrait impossible sur un run
   * bavard : chaque arrivée ramènerait la vue en bas sous les yeux du lecteur.
   */
  protected readonly suivre = signal(true);

  /**
   * Le défilement reste virtuel : même après le regroupement par tâche, un
   * run très bavard peut aligner des centaines de tâches, et `cdk-virtual-
   * scroll` continue d'éviter de rendre chaque bloc d'un coup. `itemSize`
   * suppose la hauteur REPLIÉE — un groupe déplié dépasse cette estimation,
   * ce que la stratégie à taille fixe tolère (elle rend le contenu réel,
   * seule la mesure du défilement en devient approximative pendant que ce
   * groupe précis est ouvert).
   */
  private readonly viewport = viewChild(CdkVirtualScrollViewport);

  /** Dépliages demandés À LA MAIN, en écart du défaut (voir `estDeplie`). */
  private readonly deplisManuels = signal<ReadonlySet<string>>(new Set());

  protected readonly lignes = computed<LigneFlux[]>(() => {
    const toutes = this.flux().lignes();
    const f = this.filtre();
    if (f === 'all') {
      return toutes;
    }
    // Les marques de reprise SURVIVENT au filtre : elles disent que rien n'a été
    // perdu, et cette affirmation ne dépend pas de ce qu'on regarde.
    if (f === 'results') {
      // `unknown` est l'état des events de CYCLE DE VIE d'Ansible —
      // `playbook_on_start`, `_on_task_start`, `verbose`. Ils n'ont ni machine
      // ni résultat, et ils sont les plus nombreux : sur un run mesuré, 164 de
      // ces lignes noyaient les 76 qui portent un résultat. Ce filtre les
      // écarte sans obliger à se restreindre à un seul état.
      return toutes.filter((l) => l.kind === 'resume' || l.event.state !== 'unknown');
    }
    return toutes.filter((l) => l.kind === 'resume' || l.event.state === (f as EventState));
  });

  /** Le flux regroupé PAR TÂCHE, à plat — voir `aplatir`. */
  protected readonly groupes = computed<LigneGroupee[]>(() =>
    aplatir(this.lignes(), (g) => this.estDeplie(g)),
  );

  protected readonly total = computed(
    () => this.flux().lignes().filter((l) => l.kind === 'event').length,
  );

  constructor() {
    effect(() => {
      const n = this.groupes().length;
      if (n > 0 && this.suivre()) {
        // Après le rendu du lot : demander le défilement avant que la liste
        // virtuelle ait mesuré viserait l'avant-dernière ligne.
        queueMicrotask(() => this.viewport()?.scrollToIndex(n - 1, 'auto'));
      }
    });
  }

  /**
   * Identité d'une ligne, pour la liste virtuelle.
   *
   * Le `task_number` identifie une tâche, `seq` un event ou une reprise —
   * tous deux stables sur toute la durée du run.
   */
  protected readonly suivi = (_i: number, ligne: LigneGroupee): string => {
    switch (ligne.kind) {
      case 'resume':
        return `r${ligne.seq}`;
      case 'erreur':
        return `x${ligne.seq}`;
      case 'tache':
        return `t${ligne.groupe.cle}`;
      case 'event':
        return `e${ligne.event.seq}`;
    }
  };

  /** Les colonnes du résumé d'une tâche, dans l'ordre fixé par `ORDRE_COMPTES`. */
  protected colonnesDe(g: GroupeTache): { etat: EventState; n: number }[] {
    return ORDRE_COMPTES.filter((etat) => (g.comptes[etat] ?? 0) > 0).map((etat) => ({
      etat,
      n: g.comptes[etat] ?? 0,
    }));
  }

  /**
   * Une tâche en échec s'ouvre par défaut : c'est l'information qu'on ne doit
   * jamais avoir à aller chercher derrière un clic. Le clic sert alors à la
   * REPLIER, d'où l'inversion par rapport au défaut sur `deplisManuels`.
   */
  protected estDeplie(g: GroupeTache): boolean {
    const parDefaut = g.echecs.length > 0;
    return this.deplisManuels().has(g.cle) ? !parDefaut : parDefaut;
  }

  protected basculerDepli(cle: string): void {
    this.deplisManuels.update((s) => {
      const suivant = new Set(s);
      if (suivant.has(cle)) {
        suivant.delete(cle);
      } else {
        suivant.add(cle);
      }
      return suivant;
    });
  }

  /** Le titre d'une tâche, rôle en préfixe s'il existe — la même règle que la CLI. */
  protected titreDe(g: GroupeTache): string {
    if (!g.taskName) {
      return '(tâche sans nom)';
    }
    return g.role ? `${g.role} : ${g.taskName}` : g.taskName;
  }

  /** Le module d'un event, pour la liste. Vide sur un event de cycle de vie. */
  protected moduleDe(e: EventFrame): string {
    return moduleCourt(detailDe(e.payload).module);
  }

  /** Le payload `--diff` est une lecture SÉPARÉMENT autorisée, jamais servie
   * dans le flux : il vit dans une table à contrôle d'accès distinct parce
   * qu'il contient des secrets rendus. L'ouvrir est donc une action, pas un
   * dépliage. */
  protected ouvrirDiff(e: EventFrame): void {
    this.#dialogue.open(EventDiffDialog, {
      width: 'min(880px, 92vw)',
      data: {
        runRef: this.runRef(),
        seq: e.seq,
        tache: e.task_name,
        hote: e.host,
        detail: detailDe(e.payload),
      },
    });
  }

  protected auDefilement(): void {
    const vue = this.viewport();
    if (!vue) {
      return;
    }
    // Une marge de tolérance : coller au pixel ferait décrocher le suivi au
    // moindre rebond de défilement.
    this.suivre.set(vue.measureScrollOffset('bottom') < 24);
  }

  protected retourEnBas(): void {
    this.suivre.set(true);
    this.viewport()?.scrollToIndex(this.groupes().length - 1, 'smooth');
  }
}

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

  private readonly viewport = viewChild(CdkVirtualScrollViewport);

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

  protected readonly total = computed(
    () => this.flux().lignes().filter((l) => l.kind === 'event').length,
  );

  constructor() {
    effect(() => {
      const n = this.lignes().length;
      if (n > 0 && this.suivre()) {
        // Après le rendu du lot : demander le défilement avant que la liste
        // virtuelle ait mesuré viserait l'avant-dernière ligne.
        queueMicrotask(() => this.viewport()?.scrollToIndex(n - 1, 'auto'));
      }
    });
  }

  /**
   * Identité d'une ligne.
   *
   * Le `seq` est monotone par run, donc stable ; une marque de reprise est
   * identifiée par l'offset auquel elle a eu lieu. Sans cette fonction, la liste
   * virtuelle recréerait chaque nœud à l'arrivée d'un event, ce qui annulerait
   * l'intérêt de la virtualisation sur un run bavard.
   */
  protected readonly suivi = (_i: number, ligne: LigneFlux): string =>
    ligne.kind === 'resume' ? `r${ligne.seq}` : `e${ligne.event.seq}`;

  /** Le payload `--diff` est une lecture SÉPARÉMENT autorisée, jamais servie
   * dans le flux : il vit dans une table à contrôle d'accès distinct parce
   * qu'il contient des secrets rendus. L'ouvrir est donc une action, pas un
   * dépliage. */
  /** Le module d'un event, pour la liste. Vide sur un event de cycle de vie. */
  protected moduleDe(e: EventFrame): string {
    return moduleCourt(detailDe(e.payload).module);
  }

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
    this.viewport()?.scrollToIndex(this.lignes().length - 1, 'smooth');
  }
}

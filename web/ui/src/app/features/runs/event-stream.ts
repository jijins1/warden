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
type Filtre = 'all' | 'changed' | 'failed' | 'unreachable';

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
  protected readonly filtres: Filtre[] = ['all', 'changed', 'failed', 'unreachable'];

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
  protected ouvrirDiff(e: EventFrame): void {
    this.#dialogue.open(EventDiffDialog, {
      width: 'min(880px, 92vw)',
      data: { runRef: this.runRef(), seq: e.seq, tache: e.task_name, hote: e.host },
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

import {
  ChangeDetectionStrategy,
  Component,
  computed,
  effect,
  input,
  output,
  signal,
  viewChild,
} from '@angular/core';
import { CdkVirtualScrollViewport, ScrollingModule } from '@angular/cdk/scrolling';
import { MatIconModule } from '@angular/material/icon';
import { EtatFlux, LigneFlux } from '../../../../../../core/services/run-stream/run-stream';
import { EventState } from '../../../../../../core/models/wire';
import { EndFrame, EventFrame } from '../../../../../../core/models/wire-ws';
import { aplatir, GroupeTache, LigneGroupee } from './regroupement/regroupement';
import { BarreFlux, Filtre } from './barre-flux/barre-flux';
import { LigneTache } from './ligne-tache/ligne-tache';
import { LigneEvent } from './ligne-event/ligne-event';

/**
 * Le flux d'events d'un run, regroupé par tâche.
 *
 * Présentation : il reçoit les lignes et l'état du lien, et émet l'event dont
 * on demande le diff. Il porte son propre état d'interface — la vue choisie,
 * les tâches dépliées, le suivi du bas de liste.
 */
@Component({
  selector: 'w-event-stream',
  imports: [ScrollingModule, MatIconModule, BarreFlux, LigneTache, LigneEvent],
  templateUrl: './event-stream.html',
  styleUrl: './event-stream.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class EventStream {
  readonly lignes = input.required<readonly LigneFlux[]>();
  readonly etat = input.required<EtatFlux>();
  readonly erreur = input<string | null>(null);
  readonly fin = input<EndFrame | null>(null);

  /** Un event dont on veut voir ce qu'il a fait, et son diff. */
  readonly ouvrir = output<EventFrame>();

  /** Filtre d'affichage. Purement local : le flux, lui, reçoit tout. */
  protected readonly filtre = signal<Filtre>('all');

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
  readonly #deplisManuels = signal<ReadonlySet<string>>(new Set());

  readonly #filtrees = computed<readonly LigneFlux[]>(() => {
    const toutes = this.lignes();
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
    aplatir(this.#filtrees(), (g) => this.#estDeplie(g)),
  );

  protected readonly total = computed(
    () => this.lignes().filter((l) => l.kind === 'event').length,
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

  /**
   * Une tâche en échec s'ouvre par défaut : c'est l'information qu'on ne doit
   * jamais avoir à aller chercher derrière un clic. Le clic sert alors à la
   * REPLIER, d'où l'inversion par rapport au défaut sur `deplisManuels`.
   */
  #estDeplie(g: GroupeTache): boolean {
    const parDefaut = g.echecs.length > 0;
    return this.#deplisManuels().has(g.cle) ? !parDefaut : parDefaut;
  }

  protected basculerDepli(cle: string): void {
    this.#deplisManuels.update((s) => {
      const suivant = new Set(s);
      if (suivant.has(cle)) {
        suivant.delete(cle);
      } else {
        suivant.add(cle);
      }
      return suivant;
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

import {
  ChangeDetectionStrategy,
  Component,
  Directive,
  TemplateRef,
  computed,
  contentChild,
  inject,
  input,
  linkedSignal,
} from '@angular/core';
import { DatePipe, NgTemplateOutlet } from '@angular/common';
import { RouterLink } from '@angular/router';
import { RunState } from '../../../../../core/models/wire';
import { StateChip } from '../../../../../design-system/state-chip/state-chip';
import { BadgeEnvironnement } from '../../../../badge-environnement/badge-environnement';

/** Une ligne de playbook : le run le plus récent d'un couple (dépôt, playbook). */
export interface LignePlaybook {
  readonly cle: string;
  readonly playbook: string;
  readonly repo: string;
  readonly environnement: string;
  readonly inventaire: string;
  readonly etat: RunState;
  readonly debut: string;
  readonly runRef: string;
  readonly runRefCourt: string;
}

interface ContextePanneau {
  readonly $implicit: LignePlaybook;
}

/**
 * Le contenu du panneau d'une ligne ouverte, fourni par l'appelant :
 * `<ng-template wPanneauPlaybook let-ligne>`.
 */
@Directive({ selector: 'ng-template[wPanneauPlaybook]' })
export class PanneauPlaybook {
  readonly template = inject<TemplateRef<ContextePanneau>>(TemplateRef);

  /** Type `let-ligne` pour le compilateur de templates ; jamais appelé à l'exécution. */
  static ngTemplateContextGuard(_d: PanneauPlaybook, ctx: unknown): ctx is ContextePanneau {
    return ctx !== null;
  }
}

/**
 * Les playbooks d'une machine, en table dépliable.
 *
 * Une ligne ouvre son panneau sur place plutôt que de naviguer ailleurs, et
 * une seule à la fois : rouvrir la ligne ouverte la referme, en ouvrir une
 * autre ferme la première. Le panneau porte un diff, coûteux à calculer et à
 * lire ; deux côte à côte ne se comparent pas. D'où ni « Expand all » ni
 * « Collapse all », contrairement aux autres tables dépliables.
 */
@Component({
  selector: 'w-table-playbooks',
  imports: [DatePipe, NgTemplateOutlet, RouterLink, StateChip, BadgeEnvironnement],
  templateUrl: './table-playbooks.html',
  styleUrl: './table-playbooks.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class TablePlaybooks {
  readonly lignes = input.required<readonly LignePlaybook[]>();

  protected readonly panneau = contentChild.required(PanneauPlaybook);

  /** Refermée à chaque nouvelle liste : la ligne ouverte peut ne plus y être. */
  readonly #ouverte = linkedSignal<readonly LignePlaybook[], string | null>({
    source: this.lignes,
    computation: () => null,
  });

  protected readonly vue = computed(() =>
    this.lignes().map((ligne) => ({
      ligne,
      ouverte: ligne.cle === this.#ouverte(),
      contexte: { $implicit: ligne } satisfies ContextePanneau,
    })),
  );

  protected basculer(cle: string): void {
    this.#ouverte.update((ouverte) => (ouverte === cle ? null : cle));
  }
}

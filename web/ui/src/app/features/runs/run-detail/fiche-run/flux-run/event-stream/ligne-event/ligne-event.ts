import { ChangeDetectionStrategy, Component, computed, input, output } from '@angular/core';
import { DatePipe } from '@angular/common';
import { EventFrame } from '../../../../../../../core/models/wire-ws';
import { StateChip } from '../../../../../../../design-system/state-chip/state-chip';
import { detailDe, moduleCourt } from '../../detail-event/detail-event';

/**
 * Un event sous sa tâche : numéro, état, module, hôte, heure. Le clic demande
 * ce qu'il a fait et son diff.
 */
@Component({
  selector: 'w-ligne-event',
  imports: [DatePipe, StateChip],
  templateUrl: './ligne-event.html',
  styleUrl: './ligne-event.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: { style: 'display: block' },
})
export class LigneEvent {
  readonly event = input.required<EventFrame>();

  readonly ouvrir = output<void>();

  /** Le module de l'event, sans préfixe de collection. Vide sur un event de cycle de vie. */
  protected readonly module = computed(() => moduleCourt(detailDe(this.event().payload).module));

  protected readonly change = computed(() => this.event().state === 'changed');
  protected readonly enEchec = computed(
    () => this.event().state === 'failed' || this.event().state === 'unreachable',
  );
}

import { ChangeDetectionStrategy, Component, computed, input } from '@angular/core';
import { DetailEvent } from '../../detail-event/detail-event';

/**
 * Ce que la tâche a lancé et ce que la machine a répondu : module, commande,
 * code de sortie, puis message, sortie et stderr.
 */
@Component({
  selector: 'w-sortie-tache',
  templateUrl: './sortie-tache.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
  // Ses blocs s'empilent dans le corps du dialogue et prennent son espacement.
  host: { style: 'display: contents' },
})
export class SortieTache {
  readonly detail = input.required<DetailEvent>();

  protected readonly aCode = computed(() => this.detail().code !== null);
  protected readonly codeEnEchec = computed(() => this.aCode() && this.detail().code !== 0);
  protected readonly aAction = computed(() => {
    const d = this.detail();
    return !!d.module || !!d.commande || this.aCode();
  });
}

import { ChangeDetectionStrategy, Component, computed, input, output } from '@angular/core';
import { FiltreRuns } from '../../../../../core/services/runs/runs.service';
import { Segments } from '../../../../../design-system/segments/segments';
import type { PatchFiltre } from '../recherche-runs';

/** Les états qu'un run peut prendre, en minuscules comme l'API les nomme. */
const ETATS: readonly string[] = ['running', 'succeeded', 'failed', 'interrupted'];

/**
 * Les filtres de la barre d'outils : « Changed only », « Mine only », l'état.
 *
 * Il reçoit les filtres courants et émet le changement voulu ; il ne sait pas
 * qu'ils vivent dans l'URL.
 */
@Component({
  selector: 'w-filtres-runs',
  imports: [Segments],
  templateUrl: './filtres-runs.html',
  host: { style: 'display: contents' },
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class FiltresRuns {
  readonly filtre = input.required<FiltreRuns>();

  readonly changement = output<PatchFiltre>();

  protected readonly etats = ETATS;
  protected readonly changesSeuls = computed(() => this.filtre().changed_min === 1);
  protected readonly miensSeuls = computed(() => this.filtre().mine === 1);
  protected readonly etat = computed(() => this.filtre().state ?? null);

  protected basculerChanges(): void {
    this.changement.emit({ changed_min: this.changesSeuls() ? null : 1 });
  }

  protected basculerMiens(): void {
    this.changement.emit({ mine: this.miensSeuls() ? null : 1 });
  }
}

import { ChangeDetectionStrategy, Component, computed, input, model } from '@angular/core';
import { EtatFlux } from '../../../../../../../core/services/run-stream/run-stream';

/** Les vues du flux. Purement locales : le flux, lui, reçoit tout. */
export type Filtre = 'all' | 'results' | 'changed' | 'failed' | 'unreachable';

const FILTRES: readonly Filtre[] = ['all', 'results', 'changed', 'failed', 'unreachable'];

/**
 * Ce que dit l'indicateur de lien pour chaque état du flux. La diode ne bat que
 * tant que le lien vit : seul ce qui est en cours bouge.
 */
const LIENS: Record<EtatFlux, { readonly classe: string; readonly libelle: string; readonly diode: boolean }> = {
  connecting: { classe: '', libelle: 'connecting', diode: true },
  live: { classe: 'is-live', libelle: 'live', diode: true },
  reconnecting: { classe: 'is-warn', libelle: 'reconnecting', diode: true },
  ended: { classe: 'is-ended', libelle: 'ended', diode: false },
  error: { classe: 'is-failed', libelle: 'stream error', diode: false },
};

/** La barre du flux : la vue choisie, le nombre d'events reçus, l'état du lien. */
@Component({
  selector: 'w-barre-flux',
  templateUrl: './barre-flux.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class BarreFlux {
  readonly filtre = model.required<Filtre>();
  readonly total = input.required<number>();
  readonly etat = input.required<EtatFlux>();

  protected readonly filtres = FILTRES;
  protected readonly lien = computed(() => LIENS[this.etat()]);
}

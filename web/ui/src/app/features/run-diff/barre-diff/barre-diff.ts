import { ChangeDetectionStrategy, Component, input, output } from '@angular/core';
import { MatIconModule } from '@angular/material/icon';
import { Comparaison } from '../../../core/services/diffs/diffs.service';

/** Les trois références contre lesquelles un run se compare, dans l'ordre affiché. */
const COMPARAISONS: readonly { readonly vs: Comparaison; readonly libelle: string }[] = [
  { vs: 'mainline', libelle: 'vs branch' },
  { vs: 'previous', libelle: 'vs previous run' },
  { vs: 'current', libelle: 'vs current playbook' },
];

/** La barre du diff : le choix de la référence, et « Reload » une fois un diff demandé. */
@Component({
  selector: 'w-barre-diff',
  imports: [MatIconModule],
  templateUrl: './barre-diff.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class BarreDiff {
  readonly vs = input.required<Comparaison>();
  readonly demande = input.required<boolean>();
  readonly chargement = input.required<boolean>();

  readonly choisir = output<Comparaison>();
  readonly recharger = output<void>();

  protected readonly comparaisons = COMPARAISONS;
}

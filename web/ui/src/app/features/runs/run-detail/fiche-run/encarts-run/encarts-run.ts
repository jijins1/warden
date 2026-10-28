import { ChangeDetectionStrategy, Component, input } from '@angular/core';
import { DatePipe } from '@angular/common';
import { MatIconModule } from '@angular/material/icon';
import { RunDetail } from '../../../../../core/models/wire';

/**
 * Ce qui change la lecture d'un run, avant ses faits : la rédaction échouée,
 * le code élagué. Rien quand rien ne s'applique.
 */
@Component({
  selector: 'w-encarts-run',
  imports: [DatePipe, MatIconModule],
  templateUrl: './encarts-run.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
  // Les encarts sont des blocs de la pile de la page : sans boîte à eux, ils
  // prennent son espacement, et aucun encart ne laisse aucun vide.
  host: { style: 'display: contents' },
})
export class EncartsRun {
  readonly run = input.required<RunDetail>();
}

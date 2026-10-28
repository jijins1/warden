import { ChangeDetectionStrategy, Component, computed, input } from '@angular/core';
import { DatePipe } from '@angular/common';
import { MatTooltipModule } from '@angular/material/tooltip';
import { RunDetail } from '../../../../../core/models/wire';
import { ChangedCount } from '../../../../../design-system/changed-count/changed-count';
import { Auteur } from '../../../../auteur/auteur';
import { DureePipe } from '../../../../pipes/duree/duree.pipe';

/** Les faits d'un run : qui, quoi, sur quel commit, combien de changements, quand. */
@Component({
  selector: 'w-faits-run',
  imports: [DatePipe, MatTooltipModule, ChangedCount, Auteur, DureePipe],
  templateUrl: './faits-run.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class FaitsRun {
  readonly run = input.required<RunDetail>();

  /** Le SHA tronqué à 12 ; la valeur complète reste au survol. */
  protected readonly shaCourt = computed(() => this.run().audit_sha?.slice(0, 12) ?? '');
}

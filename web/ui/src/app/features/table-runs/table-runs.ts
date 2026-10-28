import { ChangeDetectionStrategy, Component, computed, input } from '@angular/core';
import { DatePipe } from '@angular/common';
import { RouterLink } from '@angular/router';
import { MatIconModule } from '@angular/material/icon';
import { MatTooltipModule } from '@angular/material/tooltip';
import { RunListItem } from '../../core/models/wire';
import { StateChip } from '../../design-system/state-chip/state-chip';
import { ChangedCount } from '../../design-system/changed-count/changed-count';
import { BadgeEnvironnement } from '../badge-environnement/badge-environnement';
import { Auteur } from '../auteur/auteur';
import { DureePipe } from '../pipes/duree/duree.pipe';

/**
 * La table de runs, partagée par la liste des runs et la vue machine.
 *
 * De la présentation : elle reçoit des lignes et n'en charge aucune. Chaque
 * ligne est un lien vers le run, et rien d'autre n'y est cliquable — la ligne
 * a une vraie page de détail (mode simple de `DataTable`).
 */
@Component({
  selector: 'w-table-runs',
  imports: [
    DatePipe,
    RouterLink,
    MatIconModule,
    MatTooltipModule,
    StateChip,
    ChangedCount,
    BadgeEnvironnement,
    Auteur,
    DureePipe,
  ],
  templateUrl: './table-runs.html',
  styleUrl: './table-runs.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class TableRuns {
  readonly runs = input.required<readonly RunListItem[]>();

  protected readonly lignes = computed(() =>
    this.runs().map((run): LigneRun => ({ ...run, refCourte: run.RunRef.slice(0, LONGUEUR_REF) })),
  );
}

/** Un run_ref ne se tronque jamais sous 10 caractères : en deçà, deux runs se confondent. */
const LONGUEUR_REF = 10;

interface LigneRun extends RunListItem {
  readonly refCourte: string;
}

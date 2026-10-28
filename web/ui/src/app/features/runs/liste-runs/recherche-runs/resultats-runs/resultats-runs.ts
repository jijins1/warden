import { ChangeDetectionStrategy, Component, computed, input, output } from '@angular/core';
import { MatIconModule } from '@angular/material/icon';
import { RunListItem } from '../../../../../core/models/wire';
import { TableRuns } from '../../../../table-runs/table-runs';

/** Où en est la liste : ce qui est reçu, et ce qui se passe. */
export interface EtatResultats {
  runs: readonly RunListItem[];
  chargement: boolean;
  /** Faux tant que la première page n'a pas répondu : distingue « vide » de « pas encore ». */
  charge: boolean;
  erreur: string | null;
}

/**
 * Les résultats, ou l'état qui les remplace : chargement, erreur, aucun run.
 */
@Component({
  selector: 'w-resultats-runs',
  imports: [MatIconModule, TableRuns],
  templateUrl: './resultats-runs.html',
  host: { style: 'display: contents' },
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ResultatsRuns {
  readonly etat = input.required<EtatResultats>();
  readonly filtrePose = input(false);

  readonly nouvelEssai = output<void>();
  readonly effacement = output<void>();

  protected readonly runs = computed(() => this.etat().runs);
  protected readonly erreur = computed(() => this.etat().erreur);
  protected readonly avecRuns = computed(() => this.runs().length > 0);
  /** La liste est vidée à chaque rechargement : sans run, un chargement est un premier chargement. */
  protected readonly premierChargement = computed(() => this.etat().chargement && !this.avecRuns());
  protected readonly vide = computed(() => {
    const e = this.etat();
    return e.charge && !e.chargement && !e.erreur && !this.avecRuns();
  });
}

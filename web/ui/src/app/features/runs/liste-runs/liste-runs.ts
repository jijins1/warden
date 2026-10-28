import { ChangeDetectionStrategy, Component, computed, input } from '@angular/core';
import { FiltreRuns } from '../../../core/services/runs/runs.service';
import { RechercheRuns } from './recherche-runs/recherche-runs';

/**
 * Les paramètres de requête tels que l'URL les porte : des chaînes, ou rien.
 */
export interface ParametresListeRuns {
  env?: string;
  inventory?: string;
  state?: string;
  since?: string;
  until?: string;
  changed_min?: string;
  mine?: string;
}

/** Ne retient de l'URL que ce que `core.RunFilter` sait appliquer. */
export function filtreDepuisUrl(q: ParametresListeRuns): FiltreRuns {
  const changed = Number(q.changed_min);
  return {
    env: q.env || undefined,
    inventory: q.inventory || undefined,
    state: q.state || undefined,
    since: q.since || undefined,
    until: q.until || undefined,
    changed_min: Number.isFinite(changed) && changed > 0 ? changed : undefined,
    mine: q.mine === '1' ? 1 : undefined,
  };
}

/**
 * La page `/runs` : lit les filtres dans l'URL et les passe à la recherche.
 *
 * Les filtres sont dans l'URL, donc la vue se PARTAGE. Une capture d'audit
 * qu'on ne peut pas transmettre par lien oblige à décrire les filtres de vive
 * voix, et c'est là que les enquêtes divergent.
 *
 * Les inputs sont liés aux paramètres de requête par
 * `withComponentInputBinding` : ils portent donc le nom exact du paramètre,
 * `changed_min` compris.
 */
@Component({
  selector: 'w-liste-runs',
  imports: [RechercheRuns],
  template: '<w-recherche-runs [filtre]="filtre()" />',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ListeRuns {
  readonly env = input<string>();
  readonly inventory = input<string>();
  readonly state = input<string>();
  readonly since = input<string>();
  readonly until = input<string>();
  readonly changed_min = input<string>();
  readonly mine = input<string>();

  /**
   * Égal par valeur : une navigation qui ne change aucun filtre (un paramètre
   * étranger, un retour sur la même URL) ne doit pas relancer la requête.
   */
  protected readonly filtre = computed(
    () =>
      filtreDepuisUrl({
        env: this.env(),
        inventory: this.inventory(),
        state: this.state(),
        since: this.since(),
        until: this.until(),
        changed_min: this.changed_min(),
        mine: this.mine(),
      }),
    { equal: (a, b) => JSON.stringify(a) === JSON.stringify(b) },
  );
}

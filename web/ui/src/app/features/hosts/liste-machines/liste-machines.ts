import { ChangeDetectionStrategy, Component, computed, input } from '@angular/core';
import { FiltreMachines } from '../../../core/services/machines/machines.service';
import { RechercheMachines } from './recherche-machines/recherche-machines';

/**
 * La page `/hosts` : lit les filtres dans l'URL et les passe à la recherche.
 *
 * Les filtres sont dans l'URL pour la même raison que sur la liste des runs :
 * la vue se partage par lien. Les inputs sont liés aux paramètres de requête
 * par `withComponentInputBinding` et en portent donc le nom exact.
 */
@Component({
  selector: 'w-liste-machines',
  imports: [RechercheMachines],
  template: '<w-recherche-machines [filtre]="filtre()" />',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ListeMachines {
  readonly env = input<string>();
  readonly q = input<string>();

  /**
   * Égal par valeur : une navigation qui ne change aucun filtre ne doit pas
   * relancer la requête.
   */
  protected readonly filtre = computed<FiltreMachines>(
    () => ({ env: this.env() || undefined, q: this.q()?.trim() || undefined }),
    { equal: (a, b) => a.env === b.env && a.q === b.q },
  );
}

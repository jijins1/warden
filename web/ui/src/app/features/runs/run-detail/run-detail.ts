import { ChangeDetectionStrategy, Component, input } from '@angular/core';
import { FicheRun } from './fiche-run/fiche-run';

/**
 * La page d'un run, `/runs/:runRef`.
 *
 * Elle ne fait que lire la référence dans l'URL : le chargement vit dans
 * `FicheRun`, réutilisable hors de cette route.
 */
@Component({
  selector: 'w-run-detail',
  imports: [FicheRun],
  template: '<w-fiche-run [runRef]="runRef()" />',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class RunDetail {
  /** Lié depuis la route par `withComponentInputBinding`. */
  readonly runRef = input.required<string>();
}

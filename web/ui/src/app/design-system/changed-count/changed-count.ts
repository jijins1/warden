import { ChangeDetectionStrategy, Component, input } from '@angular/core';

/**
 * Le nombre de tâches `changed` d'un run : LA réponse à la seule question qui
 * compte après un run.
 *
 * Zéro s'efface en `text-faint` ; un non-zéro prend la pastille `changed`, pour
 * qu'un œil qui parcourt la colonne accroche chaque non-zéro sans lire les
 * zéros. Réservé à ce compteur-là.
 */
@Component({
  selector: 'w-changed-count',
  template: '<span class="w-count" [class.is-changed]="count() > 0">{{ count() }}</span>',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ChangedCount {
  readonly count = input.required<number>();
}

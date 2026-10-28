import { ChangeDetectionStrategy, Component, input, output } from '@angular/core';

/**
 * La pagination d'une liste paginée par curseur : « Load more » tant qu'il en
 * reste, puis « End of results ».
 *
 * Jamais un numéro de page : l'API pagine par curseur opaque, sans total, et
 * « page 3 sur 47 » inventerait les deux nombres.
 */
@Component({
  selector: 'w-pagination',
  templateUrl: './pagination.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class Pagination {
  /** Vrai tant que l'API a rendu un curseur. */
  readonly reste = input.required<boolean>();
  readonly chargement = input(false);

  readonly suite = output<void>();
}

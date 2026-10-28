import { ChangeDetectionStrategy, Component, inject, input } from '@angular/core';
import { Router } from '@angular/router';
import { FenetreMachine } from './fenetre-machine/fenetre-machine';

/**
 * La barre de l'écran machine : resserrer la fenêtre regardée.
 *
 * Ne charge rien : elle écrit la fenêtre dans l'URL, que la page relit. C'est
 * le seul endroit qui navigue sur cet écran.
 */
@Component({
  selector: 'w-outils-machine',
  imports: [FenetreMachine],
  template: '<w-fenetre-machine [since]="since()" (choisie)="fenetre($event)" />',
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: { style: 'display: contents' },
})
export class OutilsMachine {
  readonly #router = inject(Router);

  readonly host = input.required<string>();
  readonly since = input<string>();

  /**
   * Resserre la fenêtre.
   *
   * Le serveur PLAFONNE à 500 lignes et ne dit pas s'il a tronqué. Une fenêtre
   * explicite est donc le seul moyen d'être sûr de voir tout ce qui existe sur
   * l'intervalle regardé.
   */
  protected fenetre(jours: number | null): void {
    const since = jours ? new Date(Date.now() - jours * 86_400_000).toISOString() : null;
    void this.#router.navigate(['/hosts', this.host()], {
      queryParams: { since },
      queryParamsHandling: 'merge',
    });
  }
}

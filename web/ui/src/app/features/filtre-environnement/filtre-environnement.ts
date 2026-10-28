import {
  ChangeDetectionStrategy,
  Component,
  computed,
  inject,
  input,
  output,
  signal,
} from '@angular/core';
import { EnvironmentsService } from '../../core/services/environments/environments.service';
import { Segments } from '../../design-system/segments/segments';

/**
 * Le filtre d'environnement : les environnements de l'appelant, en segments.
 *
 * Un container à part, et non une donnée de la liste : ses environnements se
 * chargent seuls, et leur échec ne touche pas à la liste. Partagé par la liste
 * des runs et celle des machines, d'où sa place à la racine de `features/`.
 */
@Component({
  selector: 'w-filtre-environnement',
  imports: [Segments],
  template: `@if (disponible()) {
    <w-segments libelle="Environment" [options]="environnements()" [actif]="actif()" (choix)="choix.emit($event)" />
  }`,
  host: { style: 'display: contents' },
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class FiltreEnvironnement {
  readonly #environnements = inject(EnvironmentsService);

  readonly actif = input<string | null>(null);

  readonly choix = output<string | null>();

  /** Vide tant qu'ils ne sont pas lus : le filtre n'apparaît qu'ensuite. */
  protected readonly environnements = signal<string[]>([]);
  protected readonly disponible = computed(() => this.environnements().length > 0);

  constructor() {
    void this.#charger();
  }

  /**
   * Un échec ici n'est PAS une erreur d'écran : la liste reste juste, le filtre
   * manque. L'annoncer en rouge au-dessus d'une liste bien chargée ferait
   * douter de la liste.
   */
  async #charger(): Promise<void> {
    try {
      this.environnements.set(await this.#environnements.mesEnvironnements());
    } catch {
      this.environnements.set([]);
    }
  }
}

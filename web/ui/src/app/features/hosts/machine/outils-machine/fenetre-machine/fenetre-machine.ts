import { ChangeDetectionStrategy, Component, computed, input, output } from '@angular/core';

/**
 * Le choix de la fenêtre regardée, en jours ; `null` pour toute l'histoire.
 *
 * Seul « all » sait se dire actif : la fenêtre posée est un instant absolu
 * dans l'URL, pas un nombre de jours qu'on pourrait retrouver.
 */
@Component({
  selector: 'w-fenetre-machine',
  templateUrl: './fenetre-machine.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: { class: 'w-row' },
})
export class FenetreMachine {
  readonly since = input<string>();
  readonly choisie = output<number | null>();

  protected readonly toutes = computed(() => !this.since());
}

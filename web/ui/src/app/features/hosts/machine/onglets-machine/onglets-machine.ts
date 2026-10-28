import { ChangeDetectionStrategy, Component, computed, signal } from '@angular/core';

type Vue = 'playbooks' | 'runs';

/**
 * Les deux vues de la machine, une à la fois : les playbooks d'abord — « quels
 * playbooks ont tourné ici » plutôt que « quels runs ».
 *
 * Ne connaît que la vue choisie ; les deux blocs lui sont projetés et chargent
 * leurs données eux-mêmes.
 */
@Component({
  selector: 'w-onglets-machine',
  templateUrl: './onglets-machine.html',
  styleUrl: './onglets-machine.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: { class: 'w-stack' },
})
export class OngletsMachine {
  protected readonly vue = signal<Vue>('playbooks');
  protected readonly playbooks = computed(() => this.vue() === 'playbooks');
  protected readonly runs = computed(() => this.vue() === 'runs');
}

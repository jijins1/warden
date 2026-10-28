import { ChangeDetectionStrategy, Component, computed, input, output } from '@angular/core';

/**
 * Une rangée d'environnements ; émet celui qu'on choisit. Une liste vide dit
 * qu'il n'y a rien à gérer, plutôt qu'une rangée vide.
 */
@Component({
  selector: 'w-choix-environnement',
  templateUrl: './choix-environnement.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ChoixEnvironnement {
  readonly environnements = input.required<readonly string[]>();
  readonly selection = input<string>();
  readonly choisi = output<string>();

  protected readonly aucun = computed(() => this.environnements().length === 0);
}

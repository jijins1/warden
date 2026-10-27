import { ChangeDetectionStrategy, Component, input, output } from '@angular/core';

/** Une rangée d'environnements ; émet celui qu'on choisit. */
@Component({
  selector: 'w-choix-environnement',
  templateUrl: './choix-environnement.html',
  styleUrl: './choix-environnement.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ChoixEnvironnement {
  readonly environnements = input.required<readonly string[]>();
  readonly selection = input<string>();
  readonly choisi = output<string>();
}

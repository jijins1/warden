import { ChangeDetectionStrategy, Component, computed, input } from '@angular/core';
import { MatTooltipModule } from '@angular/material/tooltip';

/**
 * Le compte qui a lancé un run.
 *
 * Le NOM s'affiche quand il y en a un, le SUJET sinon. Le sujet est ce qui fait
 * foi en audit (ADR-0021) : quand c'est le nom qui s'affiche, le sujet reste à
 * portée, au survol.
 *
 * Deux entrées et non l'objet : `GET /runs` et `GET /runs/{run_ref}` ne le
 * sérialisent pas dans la même casse (voir `RunListItem`).
 */
@Component({
  selector: 'w-auteur',
  imports: [MatTooltipModule],
  template: `<span [matTooltip]="subject()" [matTooltipDisabled]="!nomConnu()">{{ libelle() }}</span>`,
  host: { class: 'w-truncate' },
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class Auteur {
  readonly subject = input.required<string>();
  readonly displayName = input<string>();

  protected readonly nomConnu = computed(() => !!this.displayName());
  protected readonly libelle = computed(() => this.displayName() || this.subject());
}

import { ChangeDetectionStrategy, Component, computed, input } from '@angular/core';

/**
 * L'environnement d'un run.
 *
 * `prod` est distingué visuellement, et c'est le seul nom câblé. La liste des
 * environnements est ouverte — elle vient des droits, qui viennent d'un
 * document versionné (ADR-0033) — donc en énumérer d'autres reviendrait à
 * inventer une taxonomie que le produit n'a pas. Ce qui est vrai partout, c'est
 * qu'une action sur `prod` mérite qu'on la voie avant de cliquer.
 */
@Component({
  selector: 'w-env-badge',
  template: '<span class="badge" [class.prod]="critique()">{{ env() }}</span>',
  styleUrl: './env-badge.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class EnvBadge {
  readonly env = input.required<string>();
  protected readonly critique = computed(() => this.env().toLowerCase() === 'prod');
}

import { ChangeDetectionStrategy, Component, computed, inject, input } from '@angular/core';
import { ProtectedEnvironmentsService } from '../../core/api/protected-environments.service';

/**
 * L'environnement d'un run.
 *
 * Ce qui est distingué visuellement vient de `GET /environments/protected`
 * (ADR-0044), pas du nom : avant cet ADR, seul `prod` était reconnu, câblé en
 * dur. La liste des environnements reste ouverte — elle vient des droits, qui
 * viennent d'un document versionné (ADR-0033) — donc en énumérer d'autres ici
 * reviendrait à inventer une taxonomie que le produit n'a pas. Ce qui est vrai
 * partout, c'est qu'une action sur un environnement protégé mérite qu'on la
 * voie avant de cliquer.
 */
@Component({
  selector: 'w-env-badge',
  template: '<span class="badge" [class.critique]="critique()">{{ env() }}</span>',
  styleUrl: './env-badge.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class EnvBadge {
  readonly #proteges = inject(ProtectedEnvironmentsService);

  readonly env = input.required<string>();
  protected readonly critique = computed(() => this.#proteges.isProtected(this.env()));

  constructor() {
    this.#proteges.charger();
  }
}

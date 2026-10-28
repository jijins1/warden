import { ChangeDetectionStrategy, Component, computed, inject, input } from '@angular/core';
import { ProtectedEnvironmentsService } from '../../core/services/protected-environments/protected-environments.service';
import { EnvBadge } from '../../design-system/env-badge/env-badge';

/**
 * L'environnement d'un run, distingué s'il est protégé.
 *
 * Un container : il demande le statut à `GET /environments/protected`
 * (ADR-0044) — la liste reste ouverte, elle vient des droits — et le passe au
 * badge du design system, qui ne sait qu'afficher. Partagé par toutes les
 * features qui montrent un environnement : c'est pourquoi il vit à la racine de
 * `features/`.
 */
@Component({
  selector: 'w-badge-environnement',
  imports: [EnvBadge],
  template: '<w-env-badge [env]="env()" [protege]="protege()" />',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class BadgeEnvironnement {
  readonly #proteges = inject(ProtectedEnvironmentsService);

  readonly env = input.required<string>();
  protected readonly protege = computed(() => this.#proteges.isProtected(this.env()));

  constructor() {
    this.#proteges.charger();
  }
}

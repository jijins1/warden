import { ChangeDetectionStrategy, Component, input } from '@angular/core';

/**
 * L'environnement d'un run, en mono, distingué quand il est protégé.
 *
 * Le badge ne SAIT pas ce qui est protégé : il le reçoit. Le statut vient de
 * `GET /environments/protected` (ADR-0044), jamais du nom — et le design system
 * n'appelle aucun service. Protégé, il prend le triplet `changed` et un carré
 * plein devant le nom, pour que l'état ne repose pas sur la couleur seule.
 *
 * Pas d'autres variantes (dev vert, staging bleu…) : le produit n'a pas cette
 * taxonomie.
 */
@Component({
  selector: 'w-env-badge',
  template: '<span class="w-env" [class.is-protected]="protege()">{{ env() }}</span>',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class EnvBadge {
  readonly env = input.required<string>();
  readonly protege = input(false);
}

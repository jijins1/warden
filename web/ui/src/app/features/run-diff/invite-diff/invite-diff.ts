import { ChangeDetectionStrategy, Component, output } from '@angular/core';

/** Ce que chaque comparaison répond, et le bouton qui lance le calcul. */
@Component({
  selector: 'w-invite-diff',
  templateUrl: './invite-diff.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class InviteDiff {
  readonly calculer = output<void>();
}

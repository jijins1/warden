import { ChangeDetectionStrategy, Component, computed, input } from '@angular/core';
import { MatIconModule } from '@angular/material/icon';
import { PAGE_CONNEXION } from '../../../design-system/page-connexion/page-connexion';
import { Connexion } from './connexion/connexion';

/**
 * La page de connexion. Elle lit l'URL et ne charge rien : la connexion
 * elle-même est l'affaire du container `w-connexion`.
 */
@Component({
  selector: 'w-login',
  imports: [PAGE_CONNEXION, MatIconModule, Connexion],
  templateUrl: './login.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class Login {
  /** Query param lié par `withComponentInputBinding`. */
  readonly raison = input<string>();

  /** Où revenir après connexion, posé par `authGuard`. */
  readonly retour = input<string>();

  /**
   * Pourquoi on se retrouve ici.
   *
   * Après un changement de mot de passe, la déconnexion est VOULUE et non un
   * incident : le dire évite de faire chercher une panne à quelqu'un qui vient
   * d'agir correctement.
   */
  protected readonly motDePasseChange = computed(() => this.raison() === 'password-changed');

  protected readonly destination = computed(() => this.retour() ?? '/');
}

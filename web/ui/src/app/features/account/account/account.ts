import { ChangeDetectionStrategy, Component } from '@angular/core';
import { Compte } from './compte/compte';

/**
 * La page du compte. Elle ne lit rien dans l'URL et ne charge rien : tout ce
 * qu'elle montre vient du container `w-compte`.
 *
 * Pas de template de page du design system : c'est le seul écran « mon
 * compte », il compose `w-page` et `w-page-head` directement.
 */
@Component({
  selector: 'w-account',
  imports: [Compte],
  templateUrl: './account.html',
  styleUrl: './account.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class Account {}

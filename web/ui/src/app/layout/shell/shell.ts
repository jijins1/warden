import { ChangeDetectionStrategy, Component } from '@angular/core';
import { RouterOutlet } from '@angular/router';
import { Navigation } from './navigation/navigation';

/**
 * La coquille : la barre de navigation, puis l'exutoire des routes.
 *
 * Une page : elle assemble et ne charge rien. Ce qui dépend de la session (les
 * liens d'administration, l'utilisateur, la déconnexion) vit dans
 * `Navigation`, qui est un container.
 */
@Component({
  selector: 'w-shell',
  imports: [RouterOutlet, Navigation],
  templateUrl: './shell.html',
  styleUrl: './shell.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class Shell {}

import { ChangeDetectionStrategy, Component, inject } from '@angular/core';
import { Router, RouterLink, RouterLinkActive } from '@angular/router';
import { MatIconModule } from '@angular/material/icon';
import { MatMenuModule } from '@angular/material/menu';
import { MatTooltipModule } from '@angular/material/tooltip';
import { AuthService } from '../../../core/services/auth/auth.service';
import { ThemeService } from '../../../core/services/theme/theme.service';
import { MASTHEAD } from '../../../design-system/masthead/masthead';

/**
 * La barre de navigation remplie : liens, thème, utilisateur.
 *
 * La navigation ne liste QUE ce que l'API sert. Le système de design proposait
 * « Playbooks » et « Inventories » ; aucun endpoint ne les rend, et une entrée
 * de menu qui mène à un écran vide est une promesse que le produit ne tient pas.
 *
 * Un container : il lit la session et navigue après la déconnexion, qui est la
 * suite directe d'une action qu'il traite.
 */
@Component({
  selector: 'w-navigation',
  imports: [RouterLink, RouterLinkActive, MatIconModule, MatMenuModule, MatTooltipModule, MASTHEAD],
  templateUrl: './navigation.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class Navigation {
  readonly #router = inject(Router);
  protected readonly auth = inject(AuthService);
  protected readonly themes = inject(ThemeService);

  protected async deconnecter(): Promise<void> {
    await this.auth.deconnexion();
    await this.#router.navigate(['/login']);
  }
}

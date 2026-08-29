import { ChangeDetectionStrategy, Component, inject } from '@angular/core';
import { Router, RouterLink, RouterLinkActive, RouterOutlet } from '@angular/router';
import { MatToolbarModule } from '@angular/material/toolbar';
import { MatIconModule } from '@angular/material/icon';
import { MatButtonModule } from '@angular/material/button';
import { MatMenuModule } from '@angular/material/menu';
import { MatTooltipModule } from '@angular/material/tooltip';
import { AuthService } from '../core/auth/auth.service';
import { ThemeService } from '../core/theme/theme.service';

/**
 * La coquille : barre supérieure, navigation, et l'exutoire des routes.
 *
 * La navigation ne liste QUE ce que l'API sert. Le système de design proposait
 * « Playbooks » et « Inventories » ; aucun endpoint ne les rend, et une entrée
 * de menu qui mène à un écran vide est une promesse que le produit ne tient pas.
 * Elles reviendront avec leur route.
 */
@Component({
  selector: 'w-shell',
  imports: [
    RouterOutlet,
    RouterLink,
    RouterLinkActive,
    MatToolbarModule,
    MatIconModule,
    MatButtonModule,
    MatMenuModule,
    MatTooltipModule,
  ],
  templateUrl: './shell.html',
  styleUrl: './shell.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class Shell {
  readonly #router = inject(Router);
  protected readonly auth = inject(AuthService);
  protected readonly themes = inject(ThemeService);

  protected async deconnecter(): Promise<void> {
    await this.auth.deconnexion();
    await this.#router.navigate(['/login']);
  }
}

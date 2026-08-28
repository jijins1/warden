import { ChangeDetectionStrategy, Component, computed, inject } from '@angular/core';
import { DatePipe } from '@angular/common';
import { MatIconModule } from '@angular/material/icon';
import { AuthService } from '../../core/auth/auth.service';

/**
 * Ce que le SERVEUR dit de l'appelant.
 *
 * C'est `GET /auth/whoami` rendu tel quel : le sujet, le nom, le drapeau
 * d'administration et l'échéance de session. Rien n'y est déduit côté client —
 * le cookie étant `HttpOnly`, il n'y aurait de toute façon rien à déduire.
 */
@Component({
  selector: 'w-account',
  imports: [DatePipe, MatIconModule],
  templateUrl: './account.html',
  styleUrl: './account.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class Account {
  protected readonly auth = inject(AuthService);

  /**
   * Une échéance VIDE n'est pas une donnée manquante : elle dit qu'il n'y a pas
   * de session parce qu'il n'y a pas eu de connexion — le mode de développement,
   * où l'identité est affirmée par un en-tête et non vérifiée. L'écran le nomme
   * plutôt que d'afficher un tiret.
   */
  protected readonly identiteAffirmee = computed(
    () => this.auth.connecte() && !this.auth.utilisateur()?.expires_at,
  );
}

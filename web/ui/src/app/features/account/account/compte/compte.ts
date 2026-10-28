import { ChangeDetectionStrategy, Component, computed, inject } from '@angular/core';
import { AuthService } from '../../../../core/services/auth/auth.service';
import { FaitsCompte, FaitsDuCompte } from './faits-compte/faits-compte';
import { ChangementMotDePasse } from './changement-mot-de-passe/changement-mot-de-passe';

/**
 * Ce que le SERVEUR dit de l'appelant.
 *
 * C'est `GET /auth/whoami` rendu tel quel : le sujet, le nom, le drapeau
 * d'administration et l'échéance de session. Rien n'y est déduit côté client —
 * le cookie étant `HttpOnly`, il n'y aurait de toute façon rien à déduire.
 */
@Component({
  selector: 'w-compte',
  imports: [FaitsCompte, ChangementMotDePasse],
  templateUrl: './compte.html',
  host: { style: 'display: contents' },
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class Compte {
  readonly #auth = inject(AuthService);

  /** `null` sans utilisateur : rien n'est affiché, pas même le changement de mot de passe. */
  protected readonly faits = computed<FaitsDuCompte | null>(() => {
    const moi = this.#auth.utilisateur();
    if (!moi) {
      return null;
    }
    return {
      subject: moi.subject,
      nom: moi.name ?? null,
      administrateur: moi.admin === true,
      expiration: moi.expires_at ?? null,
      // Une échéance VIDE n'est pas une donnée manquante : elle dit qu'il n'y a
      // pas de session parce qu'il n'y a pas eu de connexion — le mode de
      // développement, où l'identité est affirmée par un en-tête et non
      // vérifiée. L'écran le nomme plutôt que d'afficher un tiret.
      identiteAffirmee: !moi.expires_at,
    };
  });
}

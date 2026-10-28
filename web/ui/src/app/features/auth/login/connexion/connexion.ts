import { ChangeDetectionStrategy, Component, inject, input, signal } from '@angular/core';
import { Router } from '@angular/router';
import { AuthService } from '../../../../core/services/auth/auth.service';
import { ApiError } from '../../../../core/models/api-error';
import { ConnexionForm, Identifiants } from './connexion-form/connexion-form';

/** Ouvre la session avec les identifiants saisis, puis renvoie là d'où l'on venait. */
@Component({
  selector: 'w-connexion',
  imports: [ConnexionForm],
  template: `<w-connexion-form [enCours]="enCours()" [erreur]="erreur()" (soumis)="connecter($event)" />`,
  host: { style: 'display: contents' },
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class Connexion {
  readonly #auth = inject(AuthService);
  readonly #router = inject(Router);

  /** L'URL où revenir une fois connecté. */
  readonly retour = input.required<string>();

  protected readonly enCours = signal(false);
  protected readonly erreur = signal<string | null>(null);

  protected async connecter({ subject, password }: Identifiants): Promise<void> {
    if (this.enCours()) {
      return;
    }
    this.enCours.set(true);
    this.erreur.set(null);
    try {
      await this.#auth.connexion(subject, password);
      await this.#router.navigateByUrl(this.retour());
    } catch (e: unknown) {
      this.erreur.set(messageDe(e));
    } finally {
      this.enCours.set(false);
    }
  }
}

/**
 * Le message affiché.
 *
 * Le serveur répond « invalid credentials » sans dire laquelle des deux causes,
 * et c'est délibéré : distinguer confirmerait l'existence d'un compte. Le rendre
 * tel quel plutôt que de le reformuler en « mot de passe incorrect », qui
 * mentirait sur ce que le serveur a dit.
 */
function messageDe(e: unknown): string {
  const err = e as Partial<ApiError>;
  if (typeof err?.message === 'string' && err.message.trim() !== '') {
    return err.message;
  }
  return 'sign-in failed';
}

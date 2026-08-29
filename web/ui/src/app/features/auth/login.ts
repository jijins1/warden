import { ChangeDetectionStrategy, Component, inject, signal } from '@angular/core';
import { FormBuilder, ReactiveFormsModule, Validators } from '@angular/forms';
import { Router } from '@angular/router';
import { MatButtonModule } from '@angular/material/button';
import { MatFormFieldModule } from '@angular/material/form-field';
import { MatIconModule } from '@angular/material/icon';
import { MatInputModule } from '@angular/material/input';
import { MatProgressBarModule } from '@angular/material/progress-bar';
import { AuthService } from '../../core/auth/auth.service';
import { ApiError } from '../../core/http/api-error';

@Component({
  selector: 'w-login',
  imports: [
    ReactiveFormsModule,
    MatButtonModule,
    MatFormFieldModule,
    MatIconModule,
    MatInputModule,
    MatProgressBarModule,
  ],
  templateUrl: './login.html',
  styleUrl: './login.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class Login {
  readonly #auth = inject(AuthService);
  readonly #router = inject(Router);
  readonly #fb = inject(FormBuilder);

  protected readonly enCours = signal(false);
  protected readonly erreur = signal<string | null>(null);

  /**
   * Pourquoi on se retrouve ici.
   *
   * Après un changement de mot de passe, la déconnexion est VOULUE et non un
   * incident : le dire évite de faire chercher une panne à quelqu'un qui vient
   * d'agir correctement.
   */
  protected readonly motDePasseChange = signal(
    new URLSearchParams(location.search).get('raison') === 'password-changed',
  );

  /** Où revenir après connexion, posé par `authGuard`. */
  protected readonly retour = signal(
    new URLSearchParams(location.search).get('retour') ?? '/',
  );

  protected readonly formulaire = this.#fb.nonNullable.group({
    subject: ['', Validators.required],
    password: ['', Validators.required],
  });

  protected async soumettre(): Promise<void> {
    if (this.formulaire.invalid || this.enCours()) {
      return;
    }
    this.enCours.set(true);
    this.erreur.set(null);

    const { subject, password } = this.formulaire.getRawValue();
    try {
      await this.#auth.connexion(subject, password);
      await this.#router.navigateByUrl(this.retour());
    } catch (e: unknown) {
      this.erreur.set(messageDe(e));
      // Le mot de passe est vidé, le sujet CONSERVÉ : retaper son identifiant
      // après une faute de frappe sur le mot de passe n'apporte rien.
      this.formulaire.controls.password.reset();
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

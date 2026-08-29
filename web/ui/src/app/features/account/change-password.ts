import { ChangeDetectionStrategy, Component, inject, signal } from '@angular/core';
import { FormBuilder, ReactiveFormsModule, Validators } from '@angular/forms';
import { Router } from '@angular/router';
import { MatButtonModule } from '@angular/material/button';
import { MatFormFieldModule } from '@angular/material/form-field';
import { MatIconModule } from '@angular/material/icon';
import { MatInputModule } from '@angular/material/input';
import { AuthService } from '../../core/auth/auth.service';
import { ApiError } from '../../core/http/api-error';

/**
 * Changer son mot de passe.
 *
 * Le premier mot de passe d'un compte est GÉNÉRÉ par le serveur et montré une
 * seule fois : sans cet écran, quiconque se connecte avec n'a aucun moyen d'en
 * poser un à lui.
 *
 * Le succès DÉCONNECTE, parce que le serveur révoque toutes les sessions du
 * compte dans la même transaction. C'est annoncé avant, pas découvert après.
 */
@Component({
  selector: 'w-change-password',
  imports: [
    ReactiveFormsModule,
    MatButtonModule,
    MatFormFieldModule,
    MatIconModule,
    MatInputModule,
  ],
  templateUrl: './change-password.html',
  styleUrl: './change-password.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ChangePassword {
  readonly #auth = inject(AuthService);
  readonly #router = inject(Router);
  readonly #fb = inject(FormBuilder);

  protected readonly enCours = signal(false);
  protected readonly erreur = signal<string | null>(null);

  protected readonly formulaire = this.#fb.nonNullable.group({
    current: ['', Validators.required],
    nouveau: ['', Validators.required],
  });

  protected async soumettre(): Promise<void> {
    if (this.formulaire.invalid || this.enCours()) {
      return;
    }
    this.enCours.set(true);
    this.erreur.set(null);

    const { current, nouveau } = this.formulaire.getRawValue();
    try {
      await this.#auth.changerMotDePasse(current, nouveau);
      await this.#router.navigate(['/login'], {
        queryParams: { raison: 'password-changed' },
      });
    } catch (e: unknown) {
      const err = e as Partial<ApiError>;
      // Le 400 porte la règle de robustesse, et c'est le SEUL message que le
      // serveur veut précis — l'appelant doit savoir ce qu'on attend de lui, et
      // cette information ne renseigne personne sur l'existence d'un compte. On
      // le rend donc tel quel plutôt que de le remplacer par un générique.
      this.erreur.set(err.message ?? 'the password could not be changed');
      this.formulaire.reset();
    } finally {
      this.enCours.set(false);
    }
  }
}

import { ChangeDetectionStrategy, Component, computed, input, output } from '@angular/core';
import { toSignal } from '@angular/core/rxjs-interop';
import { FormControl, FormGroup, ReactiveFormsModule, Validators } from '@angular/forms';
import { MatIconModule } from '@angular/material/icon';

export interface Identifiants {
  subject: string;
  password: string;
}

/** Le sujet et le mot de passe. Émet les identifiants saisis, sans savoir où ils partent. */
@Component({
  selector: 'w-connexion-form',
  imports: [ReactiveFormsModule, MatIconModule],
  templateUrl: './connexion-form.html',
  host: { style: 'display: contents' },
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ConnexionForm {
  readonly enCours = input(false);
  /** Le message du serveur, rendu tel quel. */
  readonly erreur = input<string | null>(null);
  readonly soumis = output<Identifiants>();

  protected readonly formulaire = new FormGroup({
    subject: new FormControl('', { nonNullable: true, validators: Validators.required }),
    password: new FormControl('', { nonNullable: true, validators: Validators.required }),
  });

  readonly #statut = toSignal(this.formulaire.statusChanges, {
    initialValue: this.formulaire.status,
  });

  protected readonly envoiImpossible = computed(
    () => this.#statut() !== 'VALID' || this.enCours(),
  );
  protected readonly libelleBouton = computed(() => (this.enCours() ? 'Signing in…' : 'Sign in'));

  protected soumettre(): void {
    if (this.envoiImpossible()) {
      return;
    }
    this.soumis.emit(this.formulaire.getRawValue());
    // Le mot de passe est vidé, le sujet CONSERVÉ : retaper son identifiant
    // après une faute de frappe sur le mot de passe n'apporte rien. Vidé dès
    // l'envoi : en cas de succès la page s'en va, en cas d'échec il est à
    // retaper de toute façon.
    this.formulaire.controls.password.reset();
  }
}

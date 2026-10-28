import { ChangeDetectionStrategy, Component, computed, input, output } from '@angular/core';
import { toSignal } from '@angular/core/rxjs-interop';
import { FormControl, FormGroup, ReactiveFormsModule, Validators } from '@angular/forms';
import { MatIconModule } from '@angular/material/icon';

export interface MotsDePasse {
  actuel: string;
  nouveau: string;
}

/**
 * Le mot de passe actuel et le nouveau. Émet les deux, sans savoir où ils
 * partent ; annonce AVANT l'envoi que le changement déconnecte partout.
 */
@Component({
  selector: 'w-changement-mot-de-passe-form',
  imports: [ReactiveFormsModule, MatIconModule],
  templateUrl: './changement-mot-de-passe-form.html',
  host: { style: 'display: contents' },
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ChangementMotDePasseForm {
  readonly enCours = input(false);
  /** Le message du serveur, rendu tel quel. */
  readonly erreur = input<string | null>(null);
  readonly soumis = output<MotsDePasse>();

  protected readonly formulaire = new FormGroup({
    actuel: new FormControl('', { nonNullable: true, validators: Validators.required }),
    nouveau: new FormControl('', { nonNullable: true, validators: Validators.required }),
  });

  readonly #statut = toSignal(this.formulaire.statusChanges, {
    initialValue: this.formulaire.status,
  });

  protected readonly envoiImpossible = computed(
    () => this.#statut() !== 'VALID' || this.enCours(),
  );
  protected readonly libelleBouton = computed(() =>
    this.enCours() ? 'Changing…' : 'Change password',
  );

  protected soumettre(): void {
    if (this.envoiImpossible()) {
      return;
    }
    this.soumis.emit(this.formulaire.getRawValue());
    // Les mots de passe ne restent pas dans le formulaire une fois partis : en
    // cas de succès la session tombe, en cas d'échec ils sont à retaper.
    this.formulaire.reset();
  }
}

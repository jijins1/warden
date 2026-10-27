import { ChangeDetectionStrategy, Component, computed, input, output } from '@angular/core';
import { toSignal } from '@angular/core/rxjs-interop';
import { FormControl, FormGroup, ReactiveFormsModule, Validators } from '@angular/forms';
import { MatButtonModule } from '@angular/material/button';
import { MatFormFieldModule } from '@angular/material/form-field';
import { MatInputModule } from '@angular/material/input';
import { ChiffrementVault } from '../../core/api/wire';

/**
 * Un nom de variable Ansible. Le serveur ne contrôle pas la forme : le nom finit
 * en clé YAML dans le bloc rendu, et un nom qu'Ansible refuserait ne se voit
 * qu'au run suivant.
 */
const MOTIF_NOM_DE_VARIABLE = /^[A-Za-z_][A-Za-z0-9_]*$/;

/** Le nom et la valeur à chiffrer ; émet la demande validée. */
@Component({
  selector: 'w-chiffrement-vault-form',
  imports: [ReactiveFormsModule, MatButtonModule, MatFormFieldModule, MatInputModule],
  templateUrl: './chiffrement-vault-form.html',
  styleUrl: './pose-cle-vault-form.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ChiffrementVaultForm {
  readonly enCours = input(false);
  readonly soumis = output<ChiffrementVault>();

  protected readonly formulaire = new FormGroup({
    var_name: new FormControl('', {
      nonNullable: true,
      validators: [Validators.required, Validators.pattern(MOTIF_NOM_DE_VARIABLE)],
    }),
    plaintext: new FormControl('', { nonNullable: true, validators: Validators.required }),
  });

  readonly #statut = toSignal(this.formulaire.statusChanges, {
    initialValue: this.formulaire.status,
  });
  protected readonly envoiImpossible = computed(() => this.#statut() !== 'VALID' || this.enCours());

  protected soumettre(): void {
    if (this.envoiImpossible()) {
      return;
    }
    this.soumis.emit(this.formulaire.getRawValue());
    // La valeur en clair quitte le formulaire avec la requête ; le nom reste,
    // pour enchaîner sur un autre environnement sans le retaper.
    this.formulaire.controls.plaintext.reset();
  }
}

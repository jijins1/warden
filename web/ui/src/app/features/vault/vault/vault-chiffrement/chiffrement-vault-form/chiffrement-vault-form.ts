import { ChangeDetectionStrategy, Component, computed, input, output } from '@angular/core';
import { toSignal } from '@angular/core/rxjs-interop';
import { FormControl, FormGroup, ReactiveFormsModule, Validators } from '@angular/forms';
import { ChiffrementVault } from '../../../../../core/models/wire';

/**
 * Un nom de variable Ansible. Le serveur ne contrôle pas la forme : le nom finit
 * en clé YAML dans le bloc rendu, et un nom qu'Ansible refuserait ne se voit
 * qu'au run suivant.
 */
const MOTIF_NOM_DE_VARIABLE = /^[A-Za-z_][A-Za-z0-9_]*$/;

/** Le nom et la valeur à chiffrer ; émet la demande validée. */
@Component({
  selector: 'w-chiffrement-vault-form',
  imports: [ReactiveFormsModule],
  templateUrl: './chiffrement-vault-form.html',
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
  protected readonly libelle = computed(() => (this.enCours() ? 'Encrypting…' : 'Encrypt'));
  // La valeur du nom et non le statut du formulaire : le statut peut rester
  // INVALID d'une frappe à l'autre, et le signal ne se recalculerait pas.
  readonly #nom = toSignal(this.formulaire.controls.var_name.valueChanges, {
    initialValue: this.formulaire.controls.var_name.value,
  });
  /** Le motif seulement : un nom vide n'est pas une faute à signaler, juste un envoi impossible. */
  protected readonly nomInvalide = computed(
    () => this.#nom() !== '' && !MOTIF_NOM_DE_VARIABLE.test(this.#nom()),
  );

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

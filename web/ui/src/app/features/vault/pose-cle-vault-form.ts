import { ChangeDetectionStrategy, Component, computed, input, output } from '@angular/core';
import { toSignal } from '@angular/core/rxjs-interop';
import {
  AbstractControl,
  FormControl,
  FormGroup,
  ReactiveFormsModule,
  ValidationErrors,
} from '@angular/forms';
import { MatButtonModule } from '@angular/material/button';
import { MatCheckboxModule } from '@angular/material/checkbox';
import { MatFormFieldModule } from '@angular/material/form-field';
import { MatInputModule } from '@angular/material/input';
import { MatRadioModule } from '@angular/material/radio';
import { PoseCleDeVault } from '../../core/api/wire';

type Mode = 'generate' | 'import';

/**
 * Le mot de passe importé est requis en mode import, et seulement là.
 *
 * Une règle de GROUPE parce qu'elle lie deux contrôles : un validateur posé sur
 * `plaintext` seul ne saurait pas quel mode est choisi.
 */
function motDePasseSiImport(groupe: AbstractControl): ValidationErrors | null {
  const mode = groupe.get('mode')?.value as Mode;
  const plaintext = groupe.get('plaintext')?.value as string;
  return mode === 'import' && plaintext === '' ? { motDePasseRequis: true } : null;
}

/**
 * Pose ou rotation de la clé de vault d'un environnement.
 *
 * Émet la demande validée et ne sait pas où elle part. La rotation demande une
 * confirmation explicite : le contenu déjà chiffré du dépôt ne suit pas la
 * nouvelle clé (ADR-0045), et les runs suivants ne sauront plus le lire tant
 * qu'il n'est pas rechiffré.
 */
@Component({
  selector: 'w-pose-cle-vault-form',
  imports: [
    ReactiveFormsModule,
    MatButtonModule,
    MatCheckboxModule,
    MatFormFieldModule,
    MatInputModule,
    MatRadioModule,
  ],
  templateUrl: './pose-cle-vault-form.html',
  styleUrl: './pose-cle-vault-form.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class PoseCleVaultForm {
  /** Vrai quand l'environnement a déjà une clé : poser, c'est la remplacer. */
  readonly rotation = input(false);
  readonly enCours = input(false);
  readonly posee = output<PoseCleDeVault>();

  protected readonly formulaire = new FormGroup(
    {
      mode: new FormControl<Mode>('generate', { nonNullable: true }),
      plaintext: new FormControl('', { nonNullable: true }),
      confirmation: new FormControl(false, { nonNullable: true }),
    },
    { validators: motDePasseSiImport },
  );

  readonly #valeur = toSignal(this.formulaire.valueChanges, {
    initialValue: this.formulaire.getRawValue(),
  });
  readonly #valide = toSignal(this.formulaire.statusChanges, {
    initialValue: this.formulaire.status,
  });

  protected readonly modeImport = computed(() => this.#valeur().mode === 'import');
  /** Une rotation non reconnue ne part pas — voir l'en-tête. */
  protected readonly envoiImpossible = computed(
    () =>
      this.#valide() !== 'VALID' ||
      this.enCours() ||
      (this.rotation() && !this.#valeur().confirmation),
  );

  protected soumettre(): void {
    if (this.envoiImpossible()) {
      return;
    }
    const { mode, plaintext } = this.formulaire.getRawValue();
    this.posee.emit(mode === 'generate' ? { generate: true } : { plaintext });
    // Le mot de passe ne reste pas dans le formulaire une fois parti : il n'a
    // plus rien à y faire, et un second clic le renverrait.
    this.formulaire.reset();
  }
}

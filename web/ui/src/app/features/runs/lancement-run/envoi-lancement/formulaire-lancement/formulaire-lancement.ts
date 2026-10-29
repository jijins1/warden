import {
  ChangeDetectionStrategy,
  Component,
  computed,
  effect,
  input,
  output,
  untracked,
} from '@angular/core';
import { toSignal } from '@angular/core/rxjs-interop';
import {
  FormArray,
  FormControl,
  FormGroup,
  ReactiveFormsModule,
  ValidatorFn,
  Validators,
} from '@angular/forms';
import { map } from 'rxjs';
import { MatIconModule } from '@angular/material/icon';
import { OptionsLancement } from '../flags/flags';

/** Ce que l'URL peut préremplir : où, et quoi. */
export interface PreremplissageLancement {
  repo: string;
  environment: string;
  inventory: string;
  playbook: string;
}

/** La valeur validée que le formulaire émet. */
export interface ValeursLancement extends PreremplissageLancement, OptionsLancement {
  becomePassword: string;
}

/** Une ligne du tableau de variables transmises (`-e nom=valeur`, répétable). */
type LigneVariable = FormGroup<{ cle: FormControl<string>; valeur: FormControl<string> }>;

function texte(...validateurs: ValidatorFn[]): FormControl<string> {
  return new FormControl('', { nonNullable: true, validators: validateurs });
}

function ligneVariable(): LigneVariable {
  return new FormGroup({ cle: texte(), valeur: texte() });
}

/**
 * Le formulaire de lancement d'un run : où, quoi, sur quoi, comment.
 *
 * Il émet la valeur validée et ne sait pas où elle part. L'erreur du serveur et
 * l'envoi en cours lui reviennent en inputs, pour s'afficher à leur place :
 * l'erreur juste au-dessus des actions, l'envoi sur le bouton.
 */
@Component({
  selector: 'w-formulaire-lancement',
  imports: [ReactiveFormsModule, MatIconModule],
  templateUrl: './formulaire-lancement.html',
  host: { style: 'display: contents' },
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class FormulaireLancement {
  readonly valeurInitiale = input<PreremplissageLancement | null>(null);
  readonly enCours = input(false);
  readonly erreur = input<string | null>(null);

  readonly lancement = output<ValeursLancement>();
  readonly annulation = output<void>();

  protected readonly formulaire = new FormGroup({
    repo: texte(Validators.required),
    environment: texte(Validators.required),
    inventory: texte(),
    playbook: texte(Validators.required),
    check: new FormControl(false, { nonNullable: true }),
    diff: new FormControl(false, { nonNullable: true }),
    become: new FormControl(false, { nonNullable: true }),
    becomeUser: texte(),
    limit: texte(),
    tags: texte(),
    skipTags: texte(),
    verbosity: new FormControl(0, { nonNullable: true }),
    becomePassword: texte(),
    extraVars: new FormArray<LigneVariable>([]),
  });
  protected readonly variables = this.formulaire.controls.extraVars;

  /** `--become` coché : Become user et Become password apparaissent dessous. */
  protected readonly become = toSignal(this.formulaire.controls.become.valueChanges, {
    initialValue: false,
  });
  readonly #invalide = toSignal(
    this.formulaire.statusChanges.pipe(map((statut) => statut !== 'VALID')),
    { initialValue: this.formulaire.invalid },
  );
  protected readonly envoiImpossible = computed(() => this.#invalide() || this.enCours());
  protected readonly avecVariables = toSignal(
    this.variables.valueChanges.pipe(map((lignes) => lignes.length > 0)),
    { initialValue: false },
  );

  constructor() {
    // Le préremplissage vient de l'URL (lien « Launch a run » sur la liste des
    // dépôts, ou « Relaunch » sur le détail d'un run) ; il ne touche qu'aux
    // champs qu'il connaît.
    effect(() => {
      const valeur = this.valeurInitiale();
      if (valeur) {
        untracked(() => this.formulaire.patchValue(valeur));
      }
    });
  }

  protected ajouterVariable(): void {
    this.variables.push(ligneVariable());
  }

  protected retirerVariable(i: number): void {
    this.variables.removeAt(i);
  }

  protected soumettre(): void {
    if (this.formulaire.invalid || this.enCours()) {
      return;
    }
    this.lancement.emit(this.formulaire.getRawValue());
  }
}

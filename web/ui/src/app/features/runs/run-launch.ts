import { ChangeDetectionStrategy, Component, inject, signal } from '@angular/core';
import { toSignal } from '@angular/core/rxjs-interop';
import { ActivatedRoute, Router, RouterLink } from '@angular/router';
import {
  FormArray,
  FormBuilder,
  FormControl,
  FormGroup,
  ReactiveFormsModule,
  Validators,
} from '@angular/forms';
import { MatButtonModule } from '@angular/material/button';
import { MatCheckboxModule } from '@angular/material/checkbox';
import { MatFormFieldModule } from '@angular/material/form-field';
import { MatIconModule } from '@angular/material/icon';
import { MatInputModule } from '@angular/material/input';
import { MatProgressBarModule } from '@angular/material/progress-bar';
import { RunsService } from '../../core/api/runs.service';
import { ApiError } from '../../core/http/api-error';

/**
 * Les valeurs brutes du formulaire, telles que `getRawValue()` les rend.
 *
 * Un type nommé plutôt qu'inline : `composerFlags` en dépend et doit rester
 * testable seule, sans monter le composant.
 */
export interface ValeursFormulaire {
  check: boolean;
  diff: boolean;
  become: boolean;
  becomeUser: string;
  limit: string;
  /** Le NOMBRE de `v` demandés — 0 à VERBOSITE_MAX, comme `-v` à `-vvvv`. */
  verbosity: number;
  extraVars: { cle: string; valeur: string }[];
}

/**
 * Le niveau de verbosité le plus élevé que le serveur accepte.
 *
 * Miroir manuel de `core.familleVerbosite` et de ses quatre entrées
 * (`internal/server/internal/core/flags.go`), comme les types de `wire.ts` le
 * sont de leurs structures Go. Ansible accepte `-vvvvv` et au-delà ;
 * l'allowlist s'arrête à quatre, et un cinquième `v` serait refusé en 400.
 */
export const VERBOSITE_MAX = 4;

/**
 * Compose les drapeaux tels que `core.FlagsDemandes` les attend.
 *
 * Une SEULE chaîne par drapeau, `--nom=valeur` — jamais deux tokens séparés :
 * `internal/server/internal/core/flags.go` (`ValiderFlags`) coupe sur le
 * premier `=` et refuse tout le reste. Le serveur REFUSE ce qui n'est pas dans
 * son allowlist plutôt que de le filtrer (ADR-0038), donc cette fonction n'a
 * pas à deviner ce qui est accepté — juste à produire la forme exacte que
 * l'allowlist reconnaît.
 *
 * Les lignes de `extraVars` dont la clé est vide sont ignorées : un formulaire
 * qui laisse une ligne blanche ne doit pas produire `--extra-vars==valeur`.
 */
export function composerFlags(v: ValeursFormulaire): string[] {
  const flags: string[] = [];
  if (v.check) flags.push('--check');
  if (v.diff) flags.push('--diff');
  if (v.become) flags.push('--become');
  if (v.become && v.becomeUser.trim()) {
    flags.push(`--become-user=${v.becomeUser.trim()}`);
  }
  // Les `v` se répètent dans UN SEUL drapeau : l'allowlist nomme `-v`, `-vv`,
  // `-vvv` et `-vvvv`, et les range sous une clé d'unicité commune — deux
  // niveaux envoyés ensemble sont refusés, pas fusionnés.
  const niveau = Math.min(Math.max(Math.trunc(v.verbosity) || 0, 0), VERBOSITE_MAX);
  if (niveau > 0) {
    flags.push(`-${'v'.repeat(niveau)}`);
  }
  if (v.limit.trim()) {
    flags.push(`--limit=${v.limit.trim()}`);
  }
  for (const { cle, valeur } of v.extraVars) {
    if (cle.trim()) {
      flags.push(`--extra-vars=${cle.trim()}=${valeur}`);
    }
  }
  return flags;
}

/** Une ligne du tableau de variables transmises (`-e nom=valeur`, répétable). */
type LigneVariable = FormGroup<{ cle: FormControl<string>; valeur: FormControl<string> }>;

/**
 * Lance un run sur la branche protégée d'un dépôt, depuis le navigateur
 * (ADR-0042).
 *
 * PAS derrière `adminGuard`, contrairement à la liste des dépôts : lancer un
 * run demande un droit d'accès sur l'ENVIRONNEMENT (la même autorisation que
 * `warden submit`), pas un droit d'administration sur le dépôt. Un utilisateur
 * sans droit d'administration mais avec un accès `prod` doit pouvoir l'ouvrir.
 *
 * Le dépôt et l'environnement sont des champs de formulaire, préremplis depuis
 * les paramètres de requête quand ils sont connus (lien « Launch a run » sur
 * la liste des dépôts, ou « Relaunch » sur le détail d'un run) : rien
 * n'énumère les dépôts pour un utilisateur ordinaire, mais il connaît déjà les
 * siens par son propre historique de runs.
 */
@Component({
  selector: 'w-run-launch',
  imports: [
    RouterLink,
    ReactiveFormsModule,
    MatButtonModule,
    MatCheckboxModule,
    MatFormFieldModule,
    MatIconModule,
    MatInputModule,
    MatProgressBarModule,
  ],
  templateUrl: './run-launch.html',
  styleUrl: './run-launch.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class RunLaunch {
  readonly #runs = inject(RunsService);
  readonly #route = inject(ActivatedRoute);
  readonly #router = inject(Router);
  readonly #fb = inject(FormBuilder);

  protected readonly enCours = signal(false);
  protected readonly erreur = signal<string | null>(null);

  protected readonly parametres = toSignal(this.#route.queryParams, { requireSync: true });

  protected readonly formulaire = this.#fb.nonNullable.group({
    repo: ['', Validators.required],
    environment: ['', Validators.required],
    inventory: [''],
    playbook: ['', Validators.required],
    check: [false],
    diff: [false],
    become: [false],
    becomeUser: [''],
    limit: [''],
    verbosity: [0],
    becomePassword: [''],
    extraVars: this.#fb.array<LigneVariable>([]),
  });

  constructor() {
    const q = this.parametres();
    this.formulaire.patchValue({
      repo: q['repo'] ?? '',
      environment: q['environment'] ?? '',
      inventory: q['inventory'] ?? '',
      playbook: q['playbook'] ?? '',
    });
  }

  protected get extraVars(): FormArray<LigneVariable> {
    return this.formulaire.controls.extraVars;
  }

  protected ajouterVariable(): void {
    this.extraVars.push(this.#fb.nonNullable.group({ cle: '', valeur: '' }));
  }

  protected retirerVariable(i: number): void {
    this.extraVars.removeAt(i);
  }

  protected async soumettre(): Promise<void> {
    if (this.formulaire.invalid || this.enCours()) {
      return;
    }
    this.enCours.set(true);
    this.erreur.set(null);

    const { repo, environment, inventory, playbook, becomePassword, ...reste } =
      this.formulaire.getRawValue();
    try {
      const cree = await this.#runs.lancer(repo, {
        environment,
        inventory: inventory || undefined,
        playbook,
        flags: composerFlags(reste),
        become_password: becomePassword || undefined,
      });
      // Vers le détail : le flux d'events y est déjà branché, et c'est ce que
      // l'utilisateur veut voir. `replaceUrl` pour qu'un retour arrière ne
      // ramène pas sur un formulaire déjà soumis.
      await this.#router.navigate(['/runs', cree.run_ref], { replaceUrl: true });
    } catch (e: unknown) {
      // Les messages du serveur sont déjà écrits pour un lecteur humain — 403
      // nomme l'environnement refusé, 400 nomme le champ fautif, 409 dit
      // qu'un identifiant est déjà enregistré. Les reformuler mentirait sur ce
      // que le serveur a dit (même raisonnement que dans Login).
      const err = e as Partial<ApiError>;
      this.erreur.set(err?.message ?? 'launching the run failed');
    } finally {
      this.enCours.set(false);
    }
  }
}

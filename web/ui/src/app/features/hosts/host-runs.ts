import { ChangeDetectionStrategy, Component, effect, inject, signal } from '@angular/core';
import { DatePipe } from '@angular/common';
import { ActivatedRoute, Router, RouterLink } from '@angular/router';
import { toSignal } from '@angular/core/rxjs-interop';
import { FormBuilder, ReactiveFormsModule, Validators } from '@angular/forms';
import { MatButtonModule } from '@angular/material/button';
import { MatFormFieldModule } from '@angular/material/form-field';
import { MatIconModule } from '@angular/material/icon';
import { MatInputModule } from '@angular/material/input';
import { MatProgressBarModule } from '@angular/material/progress-bar';
import { RunsService } from '../../core/api/runs.service';
import { PlaybookSummary, RunListItem } from '../../core/api/wire';
import { ApiError } from '../../core/http/api-error';
import { StateChip } from '../../shared/state-chip/state-chip';
import { EnvBadge } from '../../shared/env-badge/env-badge';
import { DureePipe } from '../../shared/pipes/duree.pipe';
import { RunDiff } from '../runs/run-diff';

type Onglet = 'playbooks' | 'runs';

/**
 * L'écran détail d'une machine : quels playbooks y ont tourné (§6.5
 * dédupliqué, onglet par défaut), et en second onglet tout ce qui l'a touchée
 * run par run.
 *
 * Restreint à ce que l'appelant peut voir — le sien, ou tout run couvert par
 * un droit d'environnement vivant (Policy.CanViewRun) — et non la totalité de
 * l'historique de la machine : un environnement hors de ses droits reste hors
 * de portée. Ce n'est donc pas une promesse d'exhaustivité, et sur un outil
 * d'audit, croire avoir tout vu est pire que savoir qu'on n'a qu'une part.
 */
@Component({
  selector: 'w-host-runs',
  imports: [
    RouterLink,
    DatePipe,
    DureePipe,
    ReactiveFormsModule,
    StateChip,
    EnvBadge,
    RunDiff,
    MatButtonModule,
    MatFormFieldModule,
    MatIconModule,
    MatInputModule,
    MatProgressBarModule,
  ],
  templateUrl: './host-runs.html',
  styleUrl: './host-runs.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class HostRuns {
  readonly #runs = inject(RunsService);
  readonly #route = inject(ActivatedRoute);
  readonly #router = inject(Router);
  readonly #fb = inject(FormBuilder);

  protected readonly onglet = signal<Onglet>('playbooks');

  protected readonly items = signal<RunListItem[]>([]);
  protected readonly chargement = signal(false);
  protected readonly erreur = signal<string | null>(null);
  protected readonly charge = signal(false);

  protected readonly playbooks = signal<PlaybookSummary[]>([]);
  protected readonly chargementPb = signal(false);
  protected readonly erreurPb = signal<string | null>(null);
  protected readonly chargePb = signal(false);

  /** Le playbook dont la ligne « diff » est ouverte, un à la fois. */
  protected readonly selection = signal<PlaybookSummary | null>(null);

  protected readonly params = toSignal(this.#route.params, { requireSync: true });
  protected readonly requete = toSignal(this.#route.queryParams, { requireSync: true });

  protected readonly recherche = this.#fb.nonNullable.group({
    host: ['', Validators.required],
  });

  constructor() {
    effect(() => {
      const host = this.params()['host'] as string | undefined;
      this.requete();
      this.selection.set(null);
      if (host) {
        this.recherche.patchValue({ host }, { emitEvent: false });
        void this.charger(host);
        void this.chargerPlaybooks(host);
      } else {
        this.items.set([]);
        this.charge.set(false);
        this.playbooks.set([]);
        this.chargePb.set(false);
      }
    });
  }

  protected ouvrir(): void {
    const host = this.recherche.getRawValue().host.trim();
    if (host) {
      void this.#router.navigate(['/hosts', host], {
        queryParams: this.requete(),
      });
    }
  }

  /**
   * Resserre la fenêtre.
   *
   * Le serveur PLAFONNE à 500 lignes et ne dit pas s'il a tronqué. Une fenêtre
   * explicite est donc le seul moyen d'être sûr de voir tout ce qui existe sur
   * l'intervalle regardé.
   */
  protected fenetre(jours: number | null): void {
    const since = jours
      ? new Date(Date.now() - jours * 86_400_000).toISOString()
      : null;
    void this.#router.navigate([], {
      relativeTo: this.#route,
      queryParams: { since },
      queryParamsHandling: 'merge',
    });
  }

  protected selectionner(pb: PlaybookSummary): void {
    this.selection.set(this.selection()?.LastRunRef === pb.LastRunRef ? null : pb);
  }

  private async charger(host: string): Promise<void> {
    this.chargement.set(true);
    this.erreur.set(null);
    try {
      const q = this.requete();
      this.items.set(await this.#runs.parMachine(host, q['since'], q['until']));
    } catch (e: unknown) {
      this.erreur.set((e as Partial<ApiError>).message ?? 'listing runs failed');
    } finally {
      this.chargement.set(false);
      this.charge.set(true);
    }
  }

  private async chargerPlaybooks(host: string): Promise<void> {
    this.chargementPb.set(true);
    this.erreurPb.set(null);
    try {
      const q = this.requete();
      this.playbooks.set(await this.#runs.playbooksParMachine(host, q['since'], q['until']));
    } catch (e: unknown) {
      this.erreurPb.set((e as Partial<ApiError>).message ?? 'listing playbooks failed');
    } finally {
      this.chargementPb.set(false);
      this.chargePb.set(true);
    }
  }

  /** Le plafond serveur. Atteint, la réponse est peut-être incomplète. */
  protected readonly PLAFOND = 500;
}

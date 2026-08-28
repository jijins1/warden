import { ChangeDetectionStrategy, Component, inject, input, signal } from '@angular/core';
import { DatePipe } from '@angular/common';
import { RouterLink } from '@angular/router';
import { MatButtonModule } from '@angular/material/button';
import { MatIconModule } from '@angular/material/icon';
import { MatProgressBarModule } from '@angular/material/progress-bar';
import { MatTooltipModule } from '@angular/material/tooltip';
import { RunsService } from '../../core/api/runs.service';
import { RunDetail as RunDetailDTO } from '../../core/api/wire';
import { ApiError } from '../../core/http/api-error';
import { StateChip } from '../../shared/state-chip/state-chip';
import { EnvBadge } from '../../shared/env-badge/env-badge';
import { DureePipe } from '../../shared/pipes/duree.pipe';

/**
 * Un run, par sa référence.
 *
 * Alimenté par `GET /runs/{run_ref}`, ajouté en même temps que cet écran : sans
 * lui, recharger cette page n'avait aucun moyen de retrouver son run, la liste
 * étant paginée par curseur opaque.
 */
@Component({
  selector: 'w-run-detail',
  imports: [
    RouterLink,
    DatePipe,
    DureePipe,
    StateChip,
    EnvBadge,
    MatButtonModule,
    MatIconModule,
    MatProgressBarModule,
    MatTooltipModule,
  ],
  templateUrl: './run-detail.html',
  styleUrl: './run-detail.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class RunDetail {
  readonly #runs = inject(RunsService);

  /** Lié depuis la route par `withComponentInputBinding`. */
  readonly runRef = input.required<string>();

  protected readonly run = signal<RunDetailDTO | null>(null);
  protected readonly chargement = signal(true);
  protected readonly erreur = signal<string | null>(null);

  constructor() {
    // Un `effect` plutôt qu'un chargement unique : naviguer d'un run à l'autre
    // réutilise le composant, et ne recharger qu'au constructeur laisserait la
    // page afficher le run précédent.
    void this.#charger();
  }

  async #charger(): Promise<void> {
    this.chargement.set(true);
    this.erreur.set(null);
    try {
      this.run.set(await this.#runs.parReference(this.runRef()));
    } catch (e: unknown) {
      const err = e as Partial<ApiError>;
      // 404 couvre deux cas que le serveur refuse de distinguer — inexistant, ou
      // appartenant à quelqu'un d'autre. Les séparer ferait de l'endpoint un
      // oracle des références qui existent, donc le message ne tranche pas non
      // plus.
      this.erreur.set(
        err?.cause === 'not-found'
          ? 'No such run, or it is not yours to read.'
          : (err?.message ?? 'loading the run failed'),
      );
    } finally {
      this.chargement.set(false);
    }
  }
}

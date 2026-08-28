import {
  ChangeDetectionStrategy,
  Component,
  DestroyRef,
  inject,
  input,
  signal,
} from '@angular/core';
import { DatePipe } from '@angular/common';
import { RouterLink } from '@angular/router';
import { MatButtonModule } from '@angular/material/button';
import { MatIconModule } from '@angular/material/icon';
import { MatProgressBarModule } from '@angular/material/progress-bar';
import { MatTooltipModule } from '@angular/material/tooltip';
import { RunsService } from '../../core/api/runs.service';
import { APP_CONFIG } from '../../core/config/app-config';
import { RunStream, versWebSocket } from '../../core/api/run-stream';
import { EventStream } from './event-stream';
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
    EventStream,
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
  readonly #wsBase = versWebSocket(inject(APP_CONFIG).apiUrl.replace(/\/+$/, ''));

  /** Le flux, créé une fois le run connu et fermé avec la vue. */
  protected readonly flux = signal<RunStream | null>(null);

  /** Lié depuis la route par `withComponentInputBinding`. */
  readonly runRef = input.required<string>();

  protected readonly run = signal<RunDetailDTO | null>(null);
  protected readonly chargement = signal(true);
  protected readonly erreur = signal<string | null>(null);

  constructor() {
    void this.#charger();

    // Le flux est FERMÉ avec la vue. Sans cela, quitter la page laisserait la
    // socket ouverte et son réessai tourner en fond : un onglet longtemps
    // ouvert accumulerait une connexion par run consulté.
    inject(DestroyRef).onDestroy(() => this.flux()?.arreter());
  }

  async #charger(): Promise<void> {
    this.chargement.set(true);
    this.erreur.set(null);
    try {
      const r = await this.#runs.parReference(this.runRef());
      this.run.set(r);

      // Ouvert MÊME sur un run terminé : le serveur relit la table depuis
      // l'offset 0 et envoie l'historique puis le cadre de fin. Un run clos se
      // relit donc par le même chemin qu'un run en vol — il n'y a pas deux
      // codes à maintenir.
      const flux = new RunStream(
        `${this.#wsBase}/runs/${encodeURIComponent(r.run_ref)}/stream`,
      );
      flux.demarrer();
      this.flux.set(flux);
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

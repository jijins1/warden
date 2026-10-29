import {
  ChangeDetectionStrategy,
  Component,
  computed,
  effect,
  inject,
  input,
  signal,
  untracked,
} from '@angular/core';
import { RouterLink } from '@angular/router';
import { MatIconModule } from '@angular/material/icon';
import { RunsService } from '../../../../core/services/runs/runs.service';
import { RunDetail as RunDetailDTO } from '../../../../core/models/wire';
import { ApiError } from '../../../../core/models/api-error';
import { PAGE_DETAIL } from '../../../../design-system/page-detail/page-detail';
import { StateChip } from '../../../../design-system/state-chip/state-chip';
import { BadgeEnvironnement } from '../../../badge-environnement/badge-environnement';
import { RunDiff } from '../../../run-diff/run-diff';
import { EncartsRun } from './encarts-run/encarts-run';
import { FaitsRun } from './faits-run/faits-run';
import { RunParametres } from './run-parametres/run-parametres';
import { TagsRun } from './tags-run/tags-run';
import { FluxRun } from './flux-run/flux-run';

/**
 * Un run, par sa référence.
 *
 * Alimenté par `GET /runs/{run_ref}`, ajouté en même temps que cet écran : sans
 * lui, recharger cette page n'avait aucun moyen de retrouver son run, la liste
 * étant paginée par curseur opaque.
 */
@Component({
  selector: 'w-fiche-run',
  imports: [
    RouterLink,
    MatIconModule,
    ...PAGE_DETAIL,
    StateChip,
    BadgeEnvironnement,
    EncartsRun,
    FaitsRun,
    RunParametres,
    TagsRun,
    FluxRun,
    RunDiff,
  ],
  templateUrl: './fiche-run.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class FicheRun {
  readonly #runs = inject(RunsService);

  readonly runRef = input.required<string>();

  protected readonly run = signal<RunDetailDTO | null>(null);
  protected readonly chargement = signal(true);
  protected readonly erreur = signal<string | null>(null);

  /**
   * La référence dont on suit le flux, posée une fois le run lu. Remise à `null`
   * dès que la référence change : le flux du run précédent se ferme avec son
   * composant, sans attendre la réponse du suivant.
   */
  protected readonly refFlux = signal<string | null>(null);

  protected readonly titre = computed(() => {
    const r = this.run();
    return r ? r.playbook || r.kind : '';
  });

  /** Relancer n'a de sens que pour un playbook : la page de lancement n'en lance pas d'autre. */
  protected readonly relance = computed(() => {
    const r = this.run();
    if (!r?.playbook) {
      return null;
    }
    return {
      repo: r.repo,
      environment: r.environment,
      inventory: r.inventory,
      playbook: r.playbook,
    };
  });

  constructor() {
    // UN EFFET, et surtout PAS un appel depuis le constructeur.
    //
    // Une entrée `input.required()` n'est posée qu'APRÈS la construction : la
    // lire ici lève NG0950, l'exception tombe dans le `catch` ci-dessous, et
    // l'écran affiche un bandeau d'erreur sur un run parfaitement lisible.
    //
    // L'effet répare aussi un second défaut, latent : le routeur RÉUTILISE ce
    // composant d'un run à l'autre. Sans réaction au changement d'entrée, la
    // page garderait le run précédent.
    effect(() => {
      const ref = this.runRef();
      // `untracked` : l'effet ne doit dépendre que de la référence, pas des
      // signaux qu'il écrit.
      untracked(() => {
        this.refFlux.set(null);
        void this.#charger(ref);
      });
    });
  }

  async #charger(ref: string): Promise<void> {
    this.chargement.set(true);
    this.erreur.set(null);
    try {
      const r = await this.#runs.parReference(ref);
      this.run.set(r);
      this.refFlux.set(r.run_ref);
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

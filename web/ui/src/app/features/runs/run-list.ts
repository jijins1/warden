import { ChangeDetectionStrategy, Component, effect, inject, signal } from '@angular/core';
import { DatePipe } from '@angular/common';
import { ActivatedRoute, Router, RouterLink } from '@angular/router';
import { toSignal } from '@angular/core/rxjs-interop';
import { MatButtonModule } from '@angular/material/button';
import { MatIconModule } from '@angular/material/icon';
import { MatProgressBarModule } from '@angular/material/progress-bar';
import { MatTooltipModule } from '@angular/material/tooltip';
import { FiltreRuns, RunsService } from '../../core/api/runs.service';
import { RunListItem } from '../../core/api/wire';
import { ApiError } from '../../core/http/api-error';
import { StateChip } from '../../shared/state-chip/state-chip';
import { EnvBadge } from '../../shared/env-badge/env-badge';
import { DureePipe } from '../../shared/pipes/duree.pipe';

/**
 * Mes runs.
 *
 * « Mes », et le titre le dit : le serveur RESTREINT la liste à l'appelant —
 * `VisibleRunFilter` écrase le propriétaire au lieu de le valider, de sorte
 * qu'aucun paramètre ne peut élargir la portée. Intituler cet écran « Runs »
 * laisserait croire à une vue de parc, qui n'existe pas et dont l'absence est
 * une décision de politique, pas une lacune d'interface.
 */
@Component({
  selector: 'w-run-list',
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
  templateUrl: './run-list.html',
  styleUrl: './run-list.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class RunList {
  readonly #runs = inject(RunsService);
  readonly #route = inject(ActivatedRoute);
  readonly #router = inject(Router);

  protected readonly items = signal<RunListItem[]>([]);
  protected readonly curseur = signal<string | null>(null);
  protected readonly chargement = signal(false);
  protected readonly erreur = signal<string | null>(null);
  /** Faux tant que la première page n'a pas répondu : distingue « vide » de « pas encore ». */
  protected readonly charge = signal(false);

  /**
   * Les filtres sont dans l'URL, donc la vue se PARTAGE.
   *
   * Une capture d'audit qu'on ne peut pas transmettre par lien oblige à décrire
   * les filtres de vive voix, et c'est là que les enquêtes divergent.
   */
  protected readonly filtre = toSignal(this.#route.queryParams, { requireSync: true });

  constructor() {
    // Un `effect` sur les filtres, et non un chargement au constructeur : les
    // filtres vivent dans l'URL, donc les boutons précédent/suivant du
    // navigateur les changent sans que le composant soit reconstruit. Recharger
    // à l'initialisation seule afficherait les résultats de l'URL précédente
    // sous les filtres de la nouvelle — le pire des deux, puisque rien ne
    // signalerait l'écart.
    effect(() => {
      this.filtre();
      void this.recharger();
    });
  }

  /** Première page : remplace tout. */
  protected async recharger(): Promise<void> {
    this.items.set([]);
    this.curseur.set(null);
    await this.charger(undefined, true);
  }

  /** Page suivante : ajoute à la suite. Jamais un numéro de page. */
  protected async suivante(): Promise<void> {
    const c = this.curseur();
    if (c) {
      await this.charger(c, false);
    }
  }

  async #appliquer(patch: Record<string, string | number | null>): Promise<void> {
    // Naviguer SUFFIT : l'effet ci-dessus observe les filtres et recharge.
    // Appeler `recharger()` ici en plus déclencherait deux requêtes pour un
    // seul clic.
    await this.#router.navigate([], {
      relativeTo: this.#route,
      queryParams: patch,
      queryParamsHandling: 'merge',
    });
  }

  protected basculerChanges(): void {
    const actif = this.filtre()['changed_min'] === '1';
    void this.#appliquer({ changed_min: actif ? null : 1 });
  }

  protected filtrerEtat(etat: string | null): void {
    void this.#appliquer({ state: etat });
  }

  private async charger(cursor: string | undefined, remplace: boolean): Promise<void> {
    this.chargement.set(true);
    this.erreur.set(null);
    try {
      const page = await this.#runs.lister(this.#filtreCourant(), cursor);
      const recus = page.Runs ?? [];
      this.items.update((precedents) => (remplace ? recus : [...precedents, ...recus]));
      this.curseur.set(page.NextCursor || null);
    } catch (e: unknown) {
      const err = e as Partial<ApiError>;
      this.erreur.set(err?.message ?? 'listing runs failed');
    } finally {
      this.chargement.set(false);
      this.charge.set(true);
    }
  }

  /** Ne retient de l'URL que ce que `core.RunFilter` sait appliquer. */
  #filtreCourant(): FiltreRuns {
    const q = this.filtre();
    const changed = Number(q['changed_min']);
    return {
      env: q['env'] ?? undefined,
      inventory: q['inventory'] ?? undefined,
      state: q['state'] ?? undefined,
      since: q['since'] ?? undefined,
      until: q['until'] ?? undefined,
      changed_min: Number.isFinite(changed) && changed > 0 ? changed : undefined,
    };
  }
}

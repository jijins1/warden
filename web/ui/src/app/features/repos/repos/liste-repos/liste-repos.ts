import { ChangeDetectionStrategy, Component, computed, inject, signal } from '@angular/core';
import { MatIconModule } from '@angular/material/icon';
import { ReposService } from '../../../../core/services/repos/repos.service';
import { Repo } from '../../../../core/models/wire';
import { ApiError } from '../../../../core/models/api-error';
import { LigneRepo, TableRepos } from './table-repos/table-repos';

/** La liste des dépôts configurés, avec ses états de chargement. */
@Component({
  selector: 'w-liste-repos',
  imports: [MatIconModule, TableRepos],
  templateUrl: './liste-repos.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: { class: 'w-stack' },
})
export class ListeRepos {
  readonly #repos = inject(ReposService);

  protected readonly lignes = signal<LigneRepo[]>([]);
  protected readonly chargement = signal(false);
  protected readonly erreur = signal<string | null>(null);
  /** Faux tant que la première réponse n'est pas arrivée : distingue « vide » de « pas encore ». */
  protected readonly charge = signal(false);

  protected readonly vide = computed(
    () => this.charge() && !this.erreur() && this.lignes().length === 0,
  );

  constructor() {
    void this.charger();
  }

  protected async charger(): Promise<void> {
    this.chargement.set(true);
    this.erreur.set(null);
    try {
      this.lignes.set((await this.#repos.lister()).map(versLigne));
    } catch (e: unknown) {
      const err = e as Partial<ApiError>;
      this.erreur.set(err?.message ?? 'listing repos failed');
    } finally {
      this.chargement.set(false);
      this.charge.set(true);
    }
  }
}

function versLigne(repo: Repo): LigneRepo {
  return { nom: repo.name, url: repo.remote_url, mainline: repo.mainline_ref };
}

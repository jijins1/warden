import { ChangeDetectionStrategy, Component, inject, signal } from '@angular/core';
import { RouterLink } from '@angular/router';
import { MatIconModule } from '@angular/material/icon';
import { MatProgressBarModule } from '@angular/material/progress-bar';
import { MatButtonModule } from '@angular/material/button';
import { ReposService } from '../../core/api/repos.service';
import { Repo } from '../../core/api/wire';
import { ApiError } from '../../core/http/api-error';

/**
 * Les dépôts configurés.
 *
 * `GET /admin/repos` rend TOUTE la configuration — une carte de l'organisation,
 * pas une vue par utilisateur (voir le commentaire de `HandleListRepos`). C'est
 * pourquoi cet écran est derrière `adminGuard`, contrairement à « My runs ».
 */
@Component({
  selector: 'w-repo-list',
  imports: [RouterLink, MatIconModule, MatProgressBarModule, MatButtonModule],
  templateUrl: './repo-list.html',
  styleUrl: './repo-list.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class RepoList {
  readonly #repos = inject(ReposService);

  protected readonly items = signal<Repo[]>([]);
  protected readonly chargement = signal(false);
  protected readonly erreur = signal<string | null>(null);
  /** Faux tant que la première réponse n'est pas arrivée : distingue « vide » de « pas encore ». */
  protected readonly charge = signal(false);

  constructor() {
    void this.charger();
  }

  protected async charger(): Promise<void> {
    this.chargement.set(true);
    this.erreur.set(null);
    try {
      this.items.set(await this.#repos.lister());
    } catch (e: unknown) {
      const err = e as Partial<ApiError>;
      this.erreur.set(err?.message ?? 'listing repos failed');
    } finally {
      this.chargement.set(false);
      this.charge.set(true);
    }
  }
}

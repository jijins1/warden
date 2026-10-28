import { ChangeDetectionStrategy, Component, inject, input, signal } from '@angular/core';
import { Router } from '@angular/router';
import { MatIconModule } from '@angular/material/icon';
import { EnvironmentsService } from '../../../../core/services/environments/environments.service';
import { ApiError } from '../../../../core/models/api-error';
import { ChoixEnvironnement } from './choix-environnement/choix-environnement';

/** Les environnements de l'appelant, et le choix de celui dont on gère la clé. */
@Component({
  selector: 'w-vault-environnements',
  imports: [ChoixEnvironnement, MatIconModule],
  templateUrl: './vault-environnements.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class VaultEnvironnements {
  readonly #environnements = inject(EnvironmentsService);
  readonly #router = inject(Router);

  readonly selection = input<string>();

  protected readonly environnements = signal<string[] | null>(null);
  protected readonly erreur = signal<string | null>(null);

  constructor() {
    void this.#charger();
  }

  async #charger(): Promise<void> {
    try {
      this.environnements.set(await this.#environnements.mesEnvironnements());
    } catch (e: unknown) {
      this.erreur.set((e as Partial<ApiError>)?.message ?? 'listing your environments failed');
    }
  }

  protected choisir(env: string): void {
    void this.#router.navigate(['/vault', env]);
  }
}

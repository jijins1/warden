import {
  ChangeDetectionStrategy,
  Component,
  effect,
  inject,
  input,
  signal,
  untracked,
} from '@angular/core';
import { MatIconModule } from '@angular/material/icon';
import { VaultService } from '../../core/api/vault.service';
import { ChiffrementVault } from '../../core/api/wire';
import { ApiError } from '../../core/http/api-error';
import { BlocVaultCopiable } from './bloc-vault-copiable';
import { ChiffrementVaultForm } from './chiffrement-vault-form';

/** Chiffrer une valeur avec la clé d'un environnement, pour la coller dans le dépôt. */
@Component({
  selector: 'w-vault-chiffrement',
  imports: [ChiffrementVaultForm, BlocVaultCopiable, MatIconModule],
  templateUrl: './vault-chiffrement.html',
  styleUrl: './vault-blocs.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class VaultChiffrement {
  readonly #vault = inject(VaultService);

  readonly environnement = input.required<string>();

  protected readonly bloc = signal<string | null>(null);
  protected readonly enCours = signal(false);
  protected readonly erreur = signal<string | null>(null);

  constructor() {
    // Un bloc chiffré pour un environnement n'a rien à faire affiché sous un
    // autre : il ne s'y déchiffrerait pas.
    effect(() => {
      this.environnement();
      untracked(() => {
        this.bloc.set(null);
        this.erreur.set(null);
      });
    });
  }

  protected async chiffrer(demande: ChiffrementVault): Promise<void> {
    this.enCours.set(true);
    this.erreur.set(null);
    this.bloc.set(null);
    try {
      const r = await this.#vault.chiffrer(this.environnement(), demande);
      this.bloc.set(r.block);
    } catch (e: unknown) {
      this.erreur.set((e as Partial<ApiError>)?.message ?? 'encrypting the value failed');
    } finally {
      this.enCours.set(false);
    }
  }
}

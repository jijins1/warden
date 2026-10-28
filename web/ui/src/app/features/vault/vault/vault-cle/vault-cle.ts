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
import { MatIconModule } from '@angular/material/icon';
import { VaultService } from '../../../../core/services/vault/vault.service';
import { PoseCleDeVault, VaultKeyDescriptor } from '../../../../core/models/wire';
import { ApiError } from '../../../../core/models/api-error';
import { DescripteurCleVault } from './descripteur-cle-vault/descripteur-cle-vault';
import { PoseCleVaultForm } from './pose-cle-vault-form/pose-cle-vault-form';

/** La clé d'un environnement : ce qu'on en sait, et sa pose ou sa rotation. */
@Component({
  selector: 'w-vault-cle',
  imports: [DescripteurCleVault, PoseCleVaultForm, MatIconModule],
  templateUrl: './vault-cle.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class VaultCle {
  readonly #vault = inject(VaultService);

  readonly environnement = input.required<string>();

  /** `null` : l'environnement n'a pas encore de clé — un 404, pas une erreur. */
  protected readonly descripteur = signal<VaultKeyDescriptor | null>(null);
  /** Poser une clé sur un environnement qui en a une, c'est la rotation. */
  protected readonly existe = computed(() => this.descripteur() !== null);
  protected readonly charge = signal(false);
  protected readonly enCours = signal(false);
  protected readonly erreur = signal<string | null>(null);

  constructor() {
    // Un effet et non un chargement au constructeur : l'entrée n'est posée
    // qu'après, et le routeur RÉUTILISE ce composant d'un environnement à
    // l'autre (même raison que dans RunDetail).
    effect(() => {
      const env = this.environnement();
      untracked(() => void this.#charger(env));
    });
  }

  async #charger(env: string): Promise<void> {
    this.charge.set(false);
    this.erreur.set(null);
    this.descripteur.set(null);
    try {
      this.descripteur.set(await this.#vault.decrire(env));
    } catch (e: unknown) {
      const err = e as Partial<ApiError>;
      if (err?.cause !== 'not-found') {
        this.erreur.set(err?.message ?? 'reading the vault key failed');
      }
    } finally {
      this.charge.set(true);
    }
  }

  protected async poser(pose: PoseCleDeVault): Promise<void> {
    this.enCours.set(true);
    this.erreur.set(null);
    try {
      this.descripteur.set(await this.#vault.poser(this.environnement(), pose));
    } catch (e: unknown) {
      this.erreur.set((e as Partial<ApiError>)?.message ?? 'setting the vault key failed');
    } finally {
      this.enCours.set(false);
    }
  }
}

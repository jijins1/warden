import { HttpClient } from '@angular/common/http';
import { Injectable, inject } from '@angular/core';
import { firstValueFrom } from 'rxjs';
import { APP_CONFIG } from '../config/app-config';
import { BlocVault, ChiffrementVault, PoseCleDeVault, VaultKeyDescriptor } from './wire';

/**
 * La clé Ansible Vault d'un environnement (ADR-0045).
 *
 * Sous `/environments/{env}/vault` et non `/admin` : le droit qui gouverne ces
 * trois appels est celui d'un run sur l'environnement (ADR-0033). Aucun ne
 * rend la clé en clair — il n'existe pas d'appel pour la relire.
 */
@Injectable({ providedIn: 'root' })
export class VaultService {
  readonly #http = inject(HttpClient);
  readonly #base = inject(APP_CONFIG).apiUrl.replace(/\/+$/, '');

  #url(environnement: string): string {
    return `${this.#base}/environments/${encodeURIComponent(environnement)}/vault`;
  }

  /** Le descripteur. Un 404 (`cause === 'not-found'`) veut dire : pas encore de clé. */
  async decrire(environnement: string): Promise<VaultKeyDescriptor> {
    return firstValueFrom(this.#http.get<VaultKeyDescriptor>(this.#url(environnement)));
  }

  /** Pose la clé — la tire ou l'importe. Sur un environnement qui en a une, c'est la rotation. */
  async poser(environnement: string, pose: PoseCleDeVault): Promise<VaultKeyDescriptor> {
    return firstValueFrom(this.#http.post<VaultKeyDescriptor>(this.#url(environnement), pose));
  }

  /** Chiffre une valeur avec la clé de l'environnement ; la valeur ne revient jamais en clair. */
  async chiffrer(environnement: string, demande: ChiffrementVault): Promise<BlocVault> {
    return firstValueFrom(
      this.#http.post<BlocVault>(`${this.#url(environnement)}/encrypt`, demande),
    );
  }
}

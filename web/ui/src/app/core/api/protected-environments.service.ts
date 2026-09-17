import { HttpClient } from '@angular/common/http';
import { Injectable, inject, signal } from '@angular/core';
import { firstValueFrom } from 'rxjs';
import { APP_CONFIG } from '../config/app-config';
import { ProtectedEnvironments } from './wire';

/**
 * Les environnements marqués protégés (ADR-0044), chargés UNE fois et
 * partagés par tout composant qui affiche un badge d'environnement.
 *
 * `providedIn: 'root'` donne une instance unique : sans elle, chaque
 * `w-env-badge` d'une liste de runs referait le même appel réseau.
 *
 * Un échec vaut « rien de connu » et n'est pas propagé, comme
 * `AuthService.resoudre` : au pire, le badge ne se colore pas — ce n'est pas
 * un contrôle d'accès, donc dégrader en silence est correct.
 */
@Injectable({ providedIn: 'root' })
export class ProtectedEnvironmentsService {
  readonly #http = inject(HttpClient);
  readonly #base = inject(APP_CONFIG).apiUrl.replace(/\/+$/, '');

  readonly #environnements = signal<Set<string> | null>(null);
  #chargement: Promise<void> | null = null;

  /** Déclenche le chargement s'il n'a pas déjà eu lieu. Idempotent. */
  charger(): void {
    if (this.#environnements() !== null || this.#chargement) {
      return;
    }
    this.#chargement = this.#recuperer();
  }

  /** Dit si l'environnement donné est protégé. Faux tant que rien n'est chargé. */
  isProtected(env: string): boolean {
    return this.#environnements()?.has(env.toLowerCase()) ?? false;
  }

  async #recuperer(): Promise<void> {
    try {
      const res = await firstValueFrom(
        this.#http.get<ProtectedEnvironments>(`${this.#base}/environments/protected`),
      );
      this.#environnements.set(new Set(res.environments.map((e) => e.toLowerCase())));
    } catch {
      this.#environnements.set(new Set());
    }
  }
}

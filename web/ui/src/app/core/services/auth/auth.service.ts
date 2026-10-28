import { HttpClient } from '@angular/common/http';
import { Injectable, computed, inject, signal } from '@angular/core';
import { firstValueFrom } from 'rxjs';
import { APP_CONFIG } from '../../app-config/app-config';
import { Whoami } from '../../models/wire';

/**
 * L'identité de l'appelant, telle que le SERVEUR la voit.
 *
 * Le cookie de session est `HttpOnly` : le front ne peut pas l'inspecter, donc
 * il n'a AUCUN moyen local de savoir s'il est connecté. Ce n'est pas une gêne,
 * c'est la propriété — l'état d'authentification est toujours une réponse du
 * serveur et jamais une déduction du client, y compris après un rechargement.
 */
@Injectable({ providedIn: 'root' })
export class AuthService {
  readonly #http = inject(HttpClient);
  readonly #base = inject(APP_CONFIG).apiUrl.replace(/\/+$/, '');

  readonly #utilisateur = signal<Whoami | null>(null);
  readonly #resolu = signal(false);

  /** L'utilisateur courant, ou `null` s'il n'y en a pas. */
  readonly utilisateur = this.#utilisateur.asReadonly();

  /** Faux tant que le premier `whoami` n'a pas répondu. */
  readonly resolu = this.#resolu.asReadonly();

  readonly connecte = computed(() => this.#utilisateur() !== null);
  readonly admin = computed(() => this.#utilisateur()?.admin === true);

  /**
   * Demande au serveur qui il croit que nous sommes.
   *
   * Un échec vaut « personne » et n'est pas propagé : au démarrage, ne pas être
   * connecté est le cas NOMINAL, pas une panne.
   */
  async resoudre(): Promise<Whoami | null> {
    try {
      const moi = await firstValueFrom(this.#http.get<Whoami>(`${this.#base}/auth/whoami`));
      this.#utilisateur.set(moi);
      return moi;
    } catch {
      this.#utilisateur.set(null);
      return null;
    } finally {
      this.#resolu.set(true);
    }
  }

  /**
   * Ouvre une session.
   *
   * La réponse porte un jeton, dont on ne fait RIEN : c'est le chemin de la CLI.
   * Le navigateur reçoit un cookie `HttpOnly` que le front ne touche jamais, et
   * le stocker en plus reviendrait à recréer la surface d'XSS que le cookie
   * ferme.
   */
  async connexion(subject: string, password: string): Promise<Whoami> {
    await firstValueFrom(
      this.#http.post(`${this.#base}/auth/login`, { subject, password }, { responseType: 'text' }),
    );
    const moi = await this.resoudre();
    if (!moi) {
      throw new Error('session ouverte mais whoami la refuse');
    }
    return moi;
  }

  /**
   * Change le mot de passe, et DÉCONNECTE.
   *
   * Le serveur révoque toutes les sessions du compte dans la même transaction —
   * y compris celle qui fait la demande. Ce n'est pas un effet de bord à
   * corriger : changer son mot de passe parce qu'on le croit compromis doit
   * couper les sessions ouvertes ailleurs, sans quoi le changement ne protège de
   * rien. L'état local tombe donc avec, plutôt que de laisser l'interface
   * découvrir des 401 à la requête suivante.
   */
  async changerMotDePasse(actuel: string, nouveau: string): Promise<void> {
    try {
      await firstValueFrom(
        this.#http.post(
          `${this.#base}/auth/password`,
          { current_password: actuel, new_password: nouveau },
          { responseType: 'text' },
        ),
      );
    } finally {
      // Dans le `finally` : sur un 401 comme sur un succès, la session courante
      // peut être morte. Ne rien faire en cas d'échec laisserait une interface
      // qui se croit connectée alors qu'elle ne l'est plus.
      this.#utilisateur.set(null);
    }
  }

  /** Révoque la session. Le serveur expire aussi le cookie. */
  async deconnexion(): Promise<void> {
    try {
      await firstValueFrom(
        this.#http.post(`${this.#base}/auth/logout`, null, { responseType: 'text' }),
      );
    } finally {
      // L'état local tombe QUOI QU'IL ARRIVE : laisser l'interface se croire
      // connectée après un échec de révocation serait le pire des deux mondes.
      this.#utilisateur.set(null);
    }
  }
}

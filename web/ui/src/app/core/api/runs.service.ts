import { HttpClient, HttpParams } from '@angular/common/http';
import { Injectable, inject } from '@angular/core';
import { firstValueFrom } from 'rxjs';
import { APP_CONFIG } from '../config/app-config';
import { RunDetail, RunListResult } from './wire';

/**
 * Les filtres que le SERVEUR sait appliquer.
 *
 * Exactement ceux de `core.RunFilter`, ni plus ni moins. Le système de design
 * proposait « repository » et « branch » ; `RunFilter` ne les porte pas — `Repo`
 * est une colonne AFFICHABLE, pas un prédicat. Les offrir ici obligerait à
 * filtrer côté client sur une page, donc à mentir sur ce que le filtre couvre :
 * il ne verrait jamais les runs restés de l'autre côté du curseur.
 */
export interface FiltreRuns {
  env?: string;
  inventory?: string;
  state?: string;
  since?: string;
  until?: string;
  changed_min?: number;
  limit?: number;
}

@Injectable({ providedIn: 'root' })
export class RunsService {
  readonly #http = inject(HttpClient);
  readonly #base = inject(APP_CONFIG).apiUrl.replace(/\/+$/, '');

  /**
   * Une page de runs.
   *
   * `cursor` est OPAQUE et vient toujours d'un `NextCursor` précédent. Il n'y a
   * ni total ni numéro de page à en tirer : la pagination est par keyset, parce
   * que la table ne se purge jamais et qu'un OFFSET y coûterait un parcours
   * proportionnel au rang demandé.
   */
  async lister(filtre: FiltreRuns, cursor?: string): Promise<RunListResult> {
    let params = new HttpParams();
    for (const [cle, valeur] of Object.entries(filtre)) {
      if (valeur !== undefined && valeur !== null && valeur !== '') {
        params = params.set(cle, String(valeur));
      }
    }
    if (cursor) {
      params = params.set('cursor', cursor);
    }
    return firstValueFrom(
      this.#http.get<RunListResult>(`${this.#base}/runs`, { params }),
    );
  }

  /** Un run seul, par sa référence. C'est ce qui fait vivre un lien profond. */
  async parReference(runRef: string): Promise<RunDetail> {
    return firstValueFrom(
      this.#http.get<RunDetail>(`${this.#base}/runs/${encodeURIComponent(runRef)}`),
    );
  }
}

import { HttpClient, HttpParams } from '@angular/common/http';
import { Injectable, inject } from '@angular/core';
import { firstValueFrom } from 'rxjs';
import { APP_CONFIG } from '../../app-config/app-config';
import { PageMachines } from '../../models/wire';

/** Les filtres que `GET /hosts` sait appliquer, sous les noms de ses paramètres. */
export interface FiltreMachines {
  env?: string;
  /** Une sous-chaîne du nom de la machine. */
  q?: string;
  limit?: number;
}

@Injectable({ providedIn: 'root' })
export class MachinesService {
  readonly #http = inject(HttpClient);
  readonly #base = inject(APP_CONFIG).apiUrl.replace(/\/+$/, '');

  /**
   * Une page des machines enrôlées.
   *
   * Restreinte aux environnements où l'appelant a un droit vivant : être
   * administrateur ne l'élargit pas. `cursor` est OPAQUE et vient toujours d'un
   * `next_cursor` précédent, comme pour les runs.
   */
  async lister(filtre: FiltreMachines, cursor?: string): Promise<PageMachines> {
    let params = new HttpParams();
    for (const [cle, valeur] of Object.entries(filtre)) {
      if (valeur !== undefined && valeur !== null && valeur !== '') {
        params = params.set(cle, String(valeur));
      }
    }
    if (cursor) {
      params = params.set('cursor', cursor);
    }
    return firstValueFrom(this.#http.get<PageMachines>(`${this.#base}/hosts`, { params }));
  }
}

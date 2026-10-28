import { HttpClient } from '@angular/common/http';
import { Injectable, inject } from '@angular/core';
import { firstValueFrom } from 'rxjs';
import { APP_CONFIG } from '../../app-config/app-config';
import { KeyDescriptor, MesEnvironnements } from '../../models/wire';

@Injectable({ providedIn: 'root' })
export class EnvironmentsService {
  readonly #http = inject(HttpClient);
  readonly #base = inject(APP_CONFIG).apiUrl.replace(/\/+$/, '');

  /**
   * `GET /admin/keys` — l'inventaire de clés EST la liste des environnements
   * connus : un environnement sans clé n'a pas encore de moyen d'accès, donc
   * rien à administrer à son sujet.
   */
  async lister(): Promise<KeyDescriptor[]> {
    return firstValueFrom(this.#http.get<KeyDescriptor[]>(`${this.#base}/admin/keys`));
  }

  /**
   * `GET /environments` — les environnements sur lesquels l'APPELANT a un droit
   * vivant. Ouvert à tout compte, contrairement à `lister` : c'est la portée de
   * ses propres runs et de ses clés de vault, pas la carte de l'organisation.
   */
  async mesEnvironnements(): Promise<string[]> {
    const r = await firstValueFrom(
      this.#http.get<MesEnvironnements>(`${this.#base}/environments`),
    );
    return r.environments ?? [];
  }
}

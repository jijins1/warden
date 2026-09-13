import { HttpClient } from '@angular/common/http';
import { Injectable, inject } from '@angular/core';
import { firstValueFrom } from 'rxjs';
import { APP_CONFIG } from '../config/app-config';
import { Repo } from './wire';

@Injectable({ providedIn: 'root' })
export class ReposService {
  readonly #http = inject(HttpClient);
  readonly #base = inject(APP_CONFIG).apiUrl.replace(/\/+$/, '');

  /** `GET /admin/repos` — un tableau nu, sans pagination : peu de dépôts existent. */
  async lister(): Promise<Repo[]> {
    return firstValueFrom(this.#http.get<Repo[]>(`${this.#base}/admin/repos`));
  }
}

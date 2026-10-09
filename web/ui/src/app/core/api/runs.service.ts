import { HttpClient, HttpParams } from '@angular/common/http';
import { Injectable, inject } from '@angular/core';
import { firstValueFrom } from 'rxjs';
import { APP_CONFIG } from '../config/app-config';
import {
  LancementRun,
  PlaybooksParMachine,
  RunCree,
  RunDetail,
  RunListResult,
  RunsParMachine,
} from './wire';

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

  /**
   * Les runs qui ont touché une machine (§6.5).
   *
   * Restreint à ce que l'appelant peut voir : le sien, ou tout run dont il
   * détient un droit vivant sur l'environnement. Ce n'est donc PAS forcément
   * l'historique complet de la machine — un environnement qu'il ne couvre pas
   * reste hors de portée — et tout libellé qui promettrait l'exhaustivité
   * mentirait sur la portée.
   *
   * Un tableau nu, sans curseur : le serveur plafonne à 500 et attend qu'on
   * resserre la fenêtre.
   */
  async parMachine(host: string, since?: string, until?: string): Promise<RunsParMachine> {
    let params = new HttpParams();
    if (since) params = params.set('since', since);
    if (until) params = params.set('until', until);
    return firstValueFrom(
      this.#http.get<RunsParMachine>(
        `${this.#base}/hosts/${encodeURIComponent(host)}/runs`,
        { params },
      ),
    );
  }

  /**
   * Les playbooks DISTINCTS joués sur une machine — une ligne par (dépôt,
   * playbook), portant son run le plus récent. Même restriction de visibilité
   * que `parMachine`, et même absence de curseur.
   */
  async playbooksParMachine(
    host: string,
    since?: string,
    until?: string,
  ): Promise<PlaybooksParMachine> {
    let params = new HttpParams();
    if (since) params = params.set('since', since);
    if (until) params = params.set('until', until);
    return firstValueFrom(
      this.#http.get<PlaybooksParMachine>(
        `${this.#base}/hosts/${encodeURIComponent(host)}/playbooks`,
        { params },
      ),
    );
  }

  /** Un run seul, par sa référence. C'est ce qui fait vivre un lien profond. */
  async parReference(runRef: string): Promise<RunDetail> {
    return firstValueFrom(
      this.#http.get<RunDetail>(`${this.#base}/runs/${encodeURIComponent(runRef)}`),
    );
  }

  /**
   * `POST /repos/{repo}/runs` — lance un run sur la branche protégée, sans
   * arbre local à pousser (ADR-0042).
   *
   * Le dépôt est dans le CHEMIN et non dans le corps, comme pour le jeton de
   * push : c'est la ressource sous laquelle le run est créé.
   */
  async lancer(repo: string, demande: LancementRun): Promise<RunCree> {
    return firstValueFrom(
      this.#http.post<RunCree>(`${this.#base}/repos/${encodeURIComponent(repo)}/runs`, demande),
    );
  }
}

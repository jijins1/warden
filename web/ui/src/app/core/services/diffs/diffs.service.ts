import { HttpClient, HttpParams } from '@angular/common/http';
import { Injectable, inject } from '@angular/core';
import { firstValueFrom } from 'rxjs';
import { APP_CONFIG } from '../../app-config/app-config';

/**
 * Les trois questions du §6.5, et elles ne sont pas interchangeables.
 *
 * `current` est la seule des trois à ne PAS être figée : elle recalcule contre
 * le sommet ACTUEL de la branche protégée à chaque appel, alors que `mainline`
 * compare au SHA gelé par le run (ADR-0021). C'est délibéré — « qu'est-ce qui
 * a changé depuis, y compris ce qui a été poussé après coup » est exactement
 * la question qu'un diff figé ne peut pas répondre.
 */
export type Comparaison = 'mainline' | 'previous' | 'current';

@Injectable({ providedIn: 'root' })
export class DiffsService {
  readonly #http = inject(HttpClient);
  readonly #base = inject(APP_CONFIG).apiUrl.replace(/\/+$/, '');

  /**
   * Le diff git d'audit, en texte unifié.
   *
   * `mainline` répond « qu'avait-il modifié en local ? », `previous` répond
   * « qu'est-ce qui a changé depuis que ça marchait ? », `current` répond
   * « qu'est-ce qui a changé depuis, playbook d'aujourd'hui compris ? ». Trois
   * questions distinctes posées au même endpoint.
   *
   * `responseType: 'text'` est OBLIGATOIRE : la réponse est `text/plain`, et la
   * demander en JSON la ferait échouer au parsing sur un diff parfaitement
   * valide.
   *
   * Une chaîne VIDE n'est pas une absence : elle dit que les deux états
   * coïncident, ce qui est une réponse et non un manque.
   */
  async diffDuRun(runRef: string, vs: Comparaison): Promise<string> {
    return firstValueFrom(
      this.#http.get(`${this.#base}/runs/${encodeURIComponent(runRef)}/diff`, {
        params: new HttpParams().set('vs', vs),
        responseType: 'text',
      }),
    );
  }

  /**
   * Le payload `--diff` d'un event.
   *
   * `null` quand la tâche n'en a pas produit. Le contenu est de la SORTIE DE
   * MODULE — donc influencé par ce qui tourne sur la machine — et il contient
   * des secrets rendus (ADR-0017), ce qui est la raison pour laquelle il vit
   * dans une table à contrôle d'accès distinct et n'est jamais poussé dans le
   * flux. Il ne doit jamais être interprété comme du balisage.
   */
  async diffDEvent(runRef: string, seq: number): Promise<unknown> {
    return firstValueFrom(
      this.#http.get<unknown>(
        `${this.#base}/runs/${encodeURIComponent(runRef)}/events/${seq}/diff`,
      ),
    );
  }
}

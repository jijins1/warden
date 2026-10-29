import { ChangeDetectionStrategy, Component, computed, inject, input, signal } from '@angular/core';
import { MatIconModule } from '@angular/material/icon';
import { Comparaison, DiffsService } from '../../core/services/diffs/diffs.service';
import { ApiError } from '../../core/models/api-error';
import { expliquer } from './expliquer-refus/expliquer-refus';
import { BarreDiff } from './barre-diff/barre-diff';
import { InviteDiff } from './invite-diff/invite-diff';
import { ExplorateurRun } from './explorateur-run/explorateur-run';

/**
 * Le diff d'audit d'un run, contre l'une des trois références.
 *
 * Un container partagé : le détail d'un run et la vue machine s'en servent, d'où
 * sa place à la racine de `features/`. Il demande le diff au rôle `git` et
 * traduit son refus en phrase ; l'affichage est laissé à ses enfants.
 */
@Component({
  selector: 'w-run-diff',
  imports: [MatIconModule, BarreDiff, InviteDiff, ExplorateurRun],
  templateUrl: './run-diff.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class RunDiff {
  readonly runRef = input.required<string>();

  readonly #diffs = inject(DiffsService);

  protected readonly vs = signal<Comparaison>('mainline');
  readonly #brut = signal<string | null>(null);
  protected readonly chargement = signal(false);
  protected readonly indisponible = signal<string | null>(null);
  /** Faux tant qu'aucune comparaison n'a été demandée : le calcul coûte un aller au rôle `git`. */
  protected readonly demande = signal(false);

  /**
   * Le diff reçu, enveloppé : une chaîne VIDE est une réponse (les deux arbres
   * coïncident), et elle ne doit pas se perdre dans un test de vérité.
   */
  protected readonly resultat = computed(() => {
    const texte = this.#brut();
    return texte === null ? null : { texte };
  });

  protected choisir(vs: Comparaison): void {
    this.vs.set(vs);
    void this.charger();
  }

  protected async charger(): Promise<void> {
    this.demande.set(true);
    this.chargement.set(true);
    this.indisponible.set(null);
    this.#brut.set(null);
    try {
      this.#brut.set(await this.#diffs.diffDuRun(this.runRef(), this.vs()));
    } catch (e: unknown) {
      this.indisponible.set(expliquer(e as Partial<ApiError>));
    } finally {
      this.chargement.set(false);
    }
  }
}

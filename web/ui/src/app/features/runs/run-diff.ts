import { ChangeDetectionStrategy, Component, computed, inject, input, signal } from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { MatIconModule } from '@angular/material/icon';
import { MatProgressBarModule } from '@angular/material/progress-bar';
import { Comparaison, DiffsService } from '../../core/api/diffs.service';
import { ApiError } from '../../core/http/api-error';

/** Une ligne de diff unifié, classée pour l'affichage. */
interface Ligne {
  readonly texte: string;
  readonly genre: 'ajout' | 'retrait' | 'section' | 'entete' | 'contexte';
}

@Component({
  selector: 'w-run-diff',
  imports: [MatButtonModule, MatIconModule, MatProgressBarModule],
  templateUrl: './run-diff.html',
  styleUrl: './run-diff.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class RunDiff {
  readonly runRef = input.required<string>();

  readonly #diffs = inject(DiffsService);

  protected readonly vs = signal<Comparaison>('mainline');
  protected readonly brut = signal<string | null>(null);
  protected readonly chargement = signal(false);
  protected readonly indisponible = signal<string | null>(null);
  /** Faux tant qu'aucune comparaison n'a été demandée : le calcul coûte un aller au rôle `git`. */
  protected readonly demande = signal(false);

  protected readonly lignes = computed<Ligne[]>(() => {
    const texte = this.brut();
    if (!texte) {
      return [];
    }
    return texte.split('\n').map((l) => ({ texte: l, genre: classer(l) }));
  });

  protected readonly identiques = computed(() => this.brut() === '');

  protected choisir(vs: Comparaison): void {
    this.vs.set(vs);
    void this.charger();
  }

  protected async charger(): Promise<void> {
    this.demande.set(true);
    this.chargement.set(true);
    this.indisponible.set(null);
    this.brut.set(null);
    try {
      this.brut.set(await this.#diffs.diffDuRun(this.runRef(), this.vs()));
    } catch (e: unknown) {
      this.indisponible.set(expliquer(e as Partial<ApiError>));
    } finally {
      this.chargement.set(false);
    }
  }
}

function classer(ligne: string): Ligne['genre'] {
  if (ligne.startsWith('@@')) return 'section';
  // L'ordre compte : `+++` et `---` sont des en-têtes de fichier, pas des
  // modifications. Les tester APRÈS `+`/`-` les colorerait en vert et rouge.
  if (ligne.startsWith('+++') || ligne.startsWith('---')) return 'entete';
  if (ligne.startsWith('diff --git') || ligne.startsWith('index ')) return 'entete';
  if (ligne.startsWith('new file') || ligne.startsWith('deleted file')) return 'entete';
  if (ligne.startsWith('+')) return 'ajout';
  if (ligne.startsWith('-')) return 'retrait';
  return 'contexte';
}

/**
 * Ce qu'on dit quand le diff n'est pas calculable.
 *
 * Le motif vient de l'en-tête `X-Warden-Reason`, jamais d'une analyse du corps :
 * le serveur le pose exactement pour ça, et lire la phrase casserait à la
 * première reformulation.
 */
function expliquer(err: Partial<ApiError>): string {
  switch (err.reason) {
    case 'artifact_pruned':
      // ADR-0013 : le run a existé, c'est son arbre qui n'est plus là.
      return 'The code for this run was pruned. The run and its events remain; its diffs can no longer be computed.';
    case 'repo_unknown':
      return 'This run predates repository tracking, so there is nothing to diff it against.';
    case 'object_missing':
      return 'The code for this run is no longer in the repository.';
    case 'no_previous_run':
      // Le serveur CONFOND délibérément « aucun run antérieur » et « il
      // appartient à quelqu'un d'autre » : les distinguer ferait de l'endpoint
      // un oracle. Le message les confond donc aussi.
      return 'No earlier run you can read on this repository, environment and inventory.';
    default:
      break;
  }
  if (err.status === 501) {
    // Un déploiement d'audit sans accès au rôle `git` reste valide : ce n'est
    // pas une panne, et le dire comme telle enverrait chercher un incident.
    return 'Git diffs are not available in this deployment: the API has no client to the git role.';
  }
  if (err.status === 504) {
    return 'The git role did not answer in time. Try again.';
  }
  if (err.status === 502) {
    return 'The git role could not compute this diff.';
  }
  return err.message ?? 'the diff could not be computed';
}

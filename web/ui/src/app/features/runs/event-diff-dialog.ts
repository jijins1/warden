import { ChangeDetectionStrategy, Component, inject, signal } from '@angular/core';
import { MAT_DIALOG_DATA, MatDialogModule } from '@angular/material/dialog';
import { MatButtonModule } from '@angular/material/button';
import { MatIconModule } from '@angular/material/icon';
import { MatProgressBarModule } from '@angular/material/progress-bar';
import { DiffsService } from '../../core/api/diffs.service';
import { ApiError } from '../../core/http/api-error';

export interface DonneesDiffEvent {
  readonly runRef: string;
  readonly seq: number;
  readonly tache?: string;
  readonly hote?: string;

  /**
   * La sortie texte de l'event, DÉJÀ transmise par le flux.
   *
   * Elle ne coûte donc aucun aller au serveur, contrairement au diff : celui-ci
   * vit dans une table à contrôle d'accès distinct et se charge à la demande.
   *
   * Elle est expurgée à la source (ADR-0017) — le rapporteur masque le payload
   * entier avant qu'il quitte le pod, et l'abandonne si la rédaction est
   * invérifiable. L'afficher n'élargit pas la surface d'audit.
   */
  readonly sortie?: string;
}

/**
 * Le payload `--diff` d'un event.
 *
 * Ouvert en DIALOGUE et non déplié dans la liste : le défilement virtuel exige
 * des lignes de hauteur fixe, et une ligne extensible fausserait ses mesures.
 *
 * Le contenu est de la sortie de module — influencée par ce qui tourne sur la
 * machine — et il peut porter des secrets rendus. Il est donc affiché en texte,
 * jamais interprété, et rien ne le met en cache.
 */
@Component({
  selector: 'w-event-diff-dialog',
  imports: [MatDialogModule, MatButtonModule, MatIconModule, MatProgressBarModule],
  templateUrl: './event-diff-dialog.html',
  styleUrl: './event-diff-dialog.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class EventDiffDialog {
  protected readonly donnees = inject<DonneesDiffEvent>(MAT_DIALOG_DATA);
  readonly #diffs = inject(DiffsService);

  protected readonly texte = signal<string | null>(null);
  protected readonly vide = signal(false);
  protected readonly chargement = signal(true);
  protected readonly erreur = signal<string | null>(null);

  constructor() {
    void this.#charger();
  }

  async #charger(): Promise<void> {
    try {
      const payload = await this.#diffs.diffDEvent(this.donnees.runRef, this.donnees.seq);
      if (payload === null || payload === undefined) {
        // `null` est une réponse : cette tâche n'a produit aucun diff. Ce n'est
        // ni une erreur ni une absence de droit.
        this.vide.set(true);
        return;
      }
      this.texte.set(
        typeof payload === 'string' ? payload : JSON.stringify(payload, null, 2),
      );
    } catch (e: unknown) {
      this.erreur.set((e as Partial<ApiError>).message ?? 'the diff could not be read');
    } finally {
      this.chargement.set(false);
    }
  }
}

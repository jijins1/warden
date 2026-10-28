import { ChangeDetectionStrategy, Component, inject, signal } from '@angular/core';
import { MAT_DIALOG_DATA, MatDialogClose } from '@angular/material/dialog';
import { MatIconModule } from '@angular/material/icon';
import { DiffsService } from '../../../../../../core/services/diffs/diffs.service';
import { ApiError } from '../../../../../../core/models/api-error';
import { EventState } from '../../../../../../core/models/wire';
import { StateChip } from '../../../../../../design-system/state-chip/state-chip';
import { UnifiedDiff } from '../../../../../../design-system/unified-diff/unified-diff';
import { DetailEvent } from '../detail-event/detail-event';
import { SortieTache } from './sortie-tache/sortie-tache';

export interface DonneesDiffEvent {
  readonly runRef: string;
  readonly seq: number;
  readonly etat: EventState;
  readonly tache?: string;
  readonly hote?: string;

  /**
   * Ce que la tâche a fait, DÉJÀ transmis par le flux.
   *
   * Elle ne coûte donc aucun aller au serveur, contrairement au diff : celui-ci
   * vit dans une table à contrôle d'accès distinct et se charge à la demande.
   *
   * Elle est expurgée à la source (ADR-0017) — le rapporteur masque le payload
   * entier avant qu'il quitte le pod, et l'abandonne si la rédaction est
   * invérifiable. L'afficher n'élargit pas la surface d'audit.
   */
  readonly detail?: DetailEvent;
}

/**
 * Le payload `--diff` d'un event.
 *
 * Ouvert en DIALOGUE et non déplié dans la liste : le défilement virtuel exige
 * des lignes de hauteur fixe, et une ligne extensible fausserait ses mesures.
 *
 * Un container : il lit les données passées à l'ouverture et charge le diff.
 *
 * Le contenu est de la sortie de module — influencée par ce qui tourne sur la
 * machine — et il peut porter des secrets rendus. Il est donc affiché en texte,
 * jamais interprété, et rien ne le met en cache.
 */
@Component({
  selector: 'w-event-diff-dialog',
  imports: [MatDialogClose, MatIconModule, StateChip, UnifiedDiff, SortieTache],
  templateUrl: './event-diff-dialog.html',
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

import { ChangeDetectionStrategy, Component, computed, input, signal } from '@angular/core';
import { DatePipe } from '@angular/common';
import { KeyOrigin } from '../../../../../core/models/wire';
import { CopyBlock } from '../../../../../design-system/copy-block/copy-block';
import { BadgeEnvironnement } from '../../../../badge-environnement/badge-environnement';

/** La clé d'accès d'un environnement, prête à afficher. Jamais sa partie privée. */
export interface LigneEnvironnement {
  readonly environnement: string;
  readonly empreinte: string;
  readonly clePublique: string;
  readonly origine: KeyOrigin;
  readonly creation: string;
  /** `null` tant que la clé n'a jamais tourné. */
  readonly rotation: string | null;
}

/** Au-delà, la table offre « Expand all » et « Collapse all ». */
const SEUIL_OUTILS = 10;

/**
 * Les environnements et leur clé d'accès, en table dépliable : la clé publique
 * complète s'ouvre sous la ligne, à copier. Plusieurs lignes peuvent être
 * ouvertes à la fois.
 */
@Component({
  selector: 'w-table-environnements',
  imports: [DatePipe, CopyBlock, BadgeEnvironnement],
  templateUrl: './table-environnements.html',
  styleUrl: './table-environnements.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class TableEnvironnements {
  readonly lignes = input.required<readonly LigneEnvironnement[]>();

  readonly #ouvertes = signal<ReadonlySet<string>>(new Set());

  protected readonly vue = computed(() =>
    this.lignes().map((ligne) => ({
      ligne,
      ouverte: this.#ouvertes().has(ligne.environnement),
    })),
  );
  protected readonly outils = computed(() => this.lignes().length > SEUIL_OUTILS);

  protected basculer(environnement: string): void {
    this.#ouvertes.update((ouvertes) => {
      const suite = new Set(ouvertes);
      if (!suite.delete(environnement)) {
        suite.add(environnement);
      }
      return suite;
    });
  }

  protected toutOuvrir(): void {
    this.#ouvertes.set(new Set(this.lignes().map((l) => l.environnement)));
  }

  protected toutFermer(): void {
    this.#ouvertes.set(new Set());
  }
}

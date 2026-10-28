import { ChangeDetectionStrategy, Component, computed, input, signal } from '@angular/core';
import { RouterLink } from '@angular/router';

/** Un dépôt configuré, prêt à afficher. */
export interface LigneRepo {
  readonly nom: string;
  readonly url: string;
  readonly mainline: string;
}

/** Au-delà, la table offre « Expand all » et « Collapse all ». */
const SEUIL_OUTILS = 10;

/**
 * Les dépôts, en table dépliable : le détail d'un dépôt tient en quelques
 * faits, et on compare des dépôts voisins sans quitter la liste. Plusieurs
 * lignes peuvent être ouvertes à la fois.
 */
@Component({
  selector: 'w-table-repos',
  imports: [RouterLink],
  templateUrl: './table-repos.html',
  styleUrl: './table-repos.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class TableRepos {
  readonly lignes = input.required<readonly LigneRepo[]>();

  readonly #ouvertes = signal<ReadonlySet<string>>(new Set());

  protected readonly vue = computed(() =>
    this.lignes().map((ligne) => ({ ligne, ouverte: this.#ouvertes().has(ligne.nom) })),
  );
  protected readonly outils = computed(() => this.lignes().length > SEUIL_OUTILS);

  protected basculer(nom: string): void {
    this.#ouvertes.update((ouvertes) => {
      const suite = new Set(ouvertes);
      if (!suite.delete(nom)) {
        suite.add(nom);
      }
      return suite;
    });
  }

  protected toutOuvrir(): void {
    this.#ouvertes.set(new Set(this.lignes().map((l) => l.nom)));
  }

  protected toutFermer(): void {
    this.#ouvertes.set(new Set());
  }
}

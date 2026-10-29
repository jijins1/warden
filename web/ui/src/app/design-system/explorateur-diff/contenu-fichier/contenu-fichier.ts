import { ChangeDetectionStrategy, Component, computed, input } from '@angular/core';
import { MatIconModule } from '@angular/material/icon';
import { colorer, grammairePour } from '../coloration/coloration';

/**
 * Un fichier tel qu'il est demandé à l'appelant : `texte` et `erreur` tous deux
 * nuls tant qu'il se charge.
 */
export interface ContenuCharge {
  readonly chemin: string;
  readonly texte: string | null;
  readonly erreur: string | null;
}

/**
 * Un fichier non modifié, lu en entier : numéroté et coloré comme dans le diff,
 * sans fond d'ajout ni de retrait.
 *
 * Un octet nul trahit un binaire, qu'on n'affiche pas : décodé en UTF-8, il ne
 * donnerait que du bruit.
 */
@Component({
  selector: 'w-contenu-fichier',
  imports: [MatIconModule],
  templateUrl: './contenu-fichier.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ContenuFichier {
  readonly contenu = input.required<ContenuCharge>();

  protected readonly binaire = computed(() => this.contenu().texte?.includes('\0') ?? false);

  protected readonly lignes = computed(() => {
    const { chemin, texte } = this.contenu();
    if (!texte || this.binaire()) return [];
    const grammaire = grammairePour(chemin);
    // Le saut de ligne final de git n'est pas une ligne de plus.
    return texte.replace(/\n$/, '').split('\n').map((l) => colorer(l, grammaire));
  });
}

import { ChangeDetectionStrategy, Component, inject, input, signal } from '@angular/core';
import { ApiError } from '../../../core/models/api-error';
import { DiffsService } from '../../../core/services/diffs/diffs.service';
import { ContenuCharge } from '../../../design-system/explorateur-diff/contenu-fichier/contenu-fichier';
import { ArbreCharge, ExplorateurDiff } from '../../../design-system/explorateur-diff/explorateur-diff';
import { expliquer } from '../expliquer-refus/expliquer-refus';

/**
 * Le diff d'un run, et de quoi parcourir tout son code (ADR-0055).
 *
 * Un container : l'arbre ne se charge qu'à la première demande, et chaque
 * fichier intact qu'au moment où on l'ouvre. Rien n'est préchargé — le rôle
 * `git` est en instance unique, et la plupart des lectures d'un diff ne
 * sortent pas des fichiers modifiés.
 */
@Component({
  selector: 'w-explorateur-run',
  imports: [ExplorateurDiff],
  template: `<w-explorateur-diff [texte]="diff()" [parcourable]="true" [arbre]="arbre()" [contenu]="contenu()"
                                 (demanderArbre)="chargerArbre()" (demanderFichier)="chargerFichier($event)" />`,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ExplorateurRun {
  readonly runRef = input.required<string>();
  readonly diff = input.required<string>();

  readonly #diffs = inject(DiffsService);

  protected readonly arbre = signal<ArbreCharge | null>(null);
  protected readonly contenu = signal<ContenuCharge | null>(null);

  protected async chargerArbre(): Promise<void> {
    this.arbre.set({ chemins: null, tronque: false, erreur: null });
    try {
      const reponse = await this.#diffs.arbreDuRun(this.runRef());
      this.arbre.set({ chemins: reponse.files.map((f) => f.path), tronque: reponse.truncated, erreur: null });
    } catch (e: unknown) {
      this.arbre.set({ chemins: null, tronque: false, erreur: refus(e as Partial<ApiError>) });
    }
  }

  protected async chargerFichier(chemin: string): Promise<void> {
    this.contenu.set({ chemin, texte: null, erreur: null });
    let suite: ContenuCharge;
    try {
      suite = { chemin, texte: await this.#diffs.fichierDuRun(this.runRef(), chemin), erreur: null };
    } catch (e: unknown) {
      suite = { chemin, texte: null, erreur: refus(e as Partial<ApiError>) };
    }
    // Un clic sur un autre fichier pendant le chargement a la priorité : la
    // réponse arrivée en retard ne doit pas le remplacer.
    if (this.contenu()?.chemin === chemin) this.contenu.set(suite);
  }
}

function refus(err: Partial<ApiError>): string {
  // Le 501 du diff parle des diffs : ici, c'est la lecture du dépôt qui manque.
  if (err.status === 501) return 'Browsing the repository is not available in this deployment.';
  return expliquer(err);
}

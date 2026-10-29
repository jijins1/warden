import { ChangeDetectionStrategy, Component, computed, input, output, signal } from '@angular/core';
import { MatIconModule } from '@angular/material/icon';
import { UnifiedDiff } from '../unified-diff/unified-diff';
import { ArbreFichiers, EntreeArbre } from './arbre-fichiers/arbre-fichiers';
import { ContenuCharge, ContenuFichier } from './contenu-fichier/contenu-fichier';
import { DetailFichier } from './detail-fichier/detail-fichier';
import { parserDiff } from './parser-diff/parser-diff';

/** L'arbre complet du dépôt, tel que l'appelant l'a chargé. */
export interface ArbreCharge {
  readonly chemins: readonly string[] | null;
  readonly tronque: boolean;
  readonly erreur: string | null;
}

/**
 * Un diff git multi-fichiers : l'arborescence des fichiers touchés à gauche,
 * le fichier choisi à droite.
 *
 * Sans choix, ou quand le fichier choisi n'est plus dans le diff (une autre
 * comparaison vient d'être chargée), c'est le premier fichier qui s'affiche.
 *
 * Avec `parcourable`, une bascule montre TOUT le dépôt, fichiers modifiés
 * marqués. L'explorateur ne charge rien lui-même : il émet `demanderArbre` à la
 * première bascule et `demanderFichier` pour un fichier intact, et l'appelant
 * lui rend `arbre` et `contenu`.
 *
 * Un texte que le parseur ne reconnaît pas retombe sur le diff brut : mieux
 * vaut l'afficher tel quel que le faire disparaître.
 */
@Component({
  selector: 'w-explorateur-diff',
  imports: [MatIconModule, ArbreFichiers, ContenuFichier, DetailFichier, UnifiedDiff],
  templateUrl: './explorateur-diff.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ExplorateurDiff {
  readonly texte = input.required<string>();
  readonly parcourable = input(false);
  readonly arbre = input<ArbreCharge | null>(null);
  readonly contenu = input<ContenuCharge | null>(null);

  readonly demanderArbre = output<void>();
  readonly demanderFichier = output<string>();

  protected readonly toutVoir = signal(false);
  readonly #choix = signal<string | null>(null);

  protected readonly fichiers = computed(() => parserDiff(this.texte()));
  /** Une réponse VIDE dit que les deux arbres coïncident : c'est une réponse, pas un manque. */
  protected readonly identiques = computed(() => this.texte() === '');
  protected readonly brut = computed(() => !this.identiques() && !this.fichiers().length);

  /** Les fichiers de l'arbre affiché : le diff seul, ou tout le dépôt avec le diff par-dessus. */
  protected readonly entrees = computed<readonly EntreeArbre[]>(() => {
    const modifies = this.fichiers();
    const chemins = this.arbre()?.chemins;
    if (!this.toutVoir() || !chemins) return modifies;
    const parChemin = new Map(modifies.map((f) => [f.chemin, f]));
    const intacts = chemins
      .filter((c) => !parChemin.has(c))
      .map((chemin) => ({ chemin, statut: null, ajouts: 0, retraits: 0 }));
    return [...modifies, ...intacts];
  });

  protected readonly modifie = computed(() => {
    const fichiers = this.fichiers();
    const choix = this.#choix();
    const trouve = fichiers.find((f) => f.chemin === choix);
    if (trouve) return trouve;
    // En mode « tout », un choix hors du diff est un fichier intact, pas un choix périmé.
    return this.toutVoir() && choix !== null ? null : (fichiers[0] ?? null);
  });

  /** Le chemin sélectionné : un fichier du diff, ou un fichier intact en mode « tout ». */
  protected readonly selection = computed(() => {
    const choix = this.#choix();
    if (this.toutVoir() && choix !== null) return choix;
    return this.modifie()?.chemin ?? null;
  });

  protected readonly intact = computed(() => {
    const c = this.contenu();
    const choix = this.selection();
    return this.toutVoir() && !this.modifie() && c?.chemin === choix ? c : null;
  });

  protected readonly totaux = computed(() =>
    this.fichiers().reduce((t, f) => ({ ajouts: t.ajouts + f.ajouts, retraits: t.retraits + f.retraits }), {
      ajouts: 0,
      retraits: 0,
    }),
  );

  protected basculer(tout: boolean): void {
    this.toutVoir.set(tout);
    if (tout && !this.arbre()) this.demanderArbre.emit();
    // Un fichier intact choisi n'a pas de sens dans la vue du diff seul.
    if (!tout && !this.fichiers().some((f) => f.chemin === this.#choix())) this.#choix.set(null);
  }

  protected choisir(chemin: string): void {
    this.#choix.set(chemin);
    if (!this.fichiers().some((f) => f.chemin === chemin) && this.contenu()?.chemin !== chemin) {
      this.demanderFichier.emit(chemin);
    }
  }
}

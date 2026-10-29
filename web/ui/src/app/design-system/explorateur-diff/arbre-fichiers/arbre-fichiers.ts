import { ChangeDetectionStrategy, Component, computed, input, output, signal } from '@angular/core';
import { MatIconModule } from '@angular/material/icon';
import { StatutFichier } from '../parser-diff/parser-diff';

/**
 * Un fichier de l'arbre. Un statut `null` dit que le fichier n'a pas changé :
 * l'arbre montre alors tout le dépôt, pas seulement le diff.
 */
export interface EntreeArbre {
  readonly chemin: string;
  readonly statut: StatutFichier | null;
  readonly ajouts: number;
  readonly retraits: number;
}

interface Noeud {
  readonly nom: string;
  readonly chemin: string;
  readonly enfants: Noeud[];
  readonly fichier: EntreeArbre | null;
  /** Vrai si un fichier modifié vit sous ce dossier. */
  readonly modifie: boolean;
}

/** Une ligne affichée de l'arbre : un dossier ou un fichier, à sa profondeur. */
interface Rangee {
  readonly nom: string;
  readonly chemin: string;
  readonly profondeur: number;
  readonly fichier: EntreeArbre | null;
  readonly replie: boolean;
}

/** La lettre de statut, celle de `git status --short`. */
const LETTRES: Record<StatutFichier, string> = { added: 'A', deleted: 'D', modified: 'M', renamed: 'R' };

/**
 * Des fichiers en arborescence : dossiers repliables, fichiers modifiés marqués
 * de leur statut et de leurs +/−.
 *
 * Une chaîne de dossiers à enfant unique tient sur une rangée (`roles/web/tasks`),
 * sans quoi l'arbre d'un dépôt Ansible s'étire avant d'atteindre un fichier.
 *
 * Avec `repliParDefaut`, seuls les dossiers qui contiennent une modification
 * s'ouvrent d'office : un dépôt entier déplié noierait les quelques fichiers
 * que le diff touche.
 */
@Component({
  selector: 'w-arbre-fichiers',
  imports: [MatIconModule],
  templateUrl: './arbre-fichiers.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ArbreFichiers {
  readonly fichiers = input.required<readonly EntreeArbre[]>();
  readonly selection = input.required<string | null>();
  readonly repliParDefaut = input(false);

  readonly choisir = output<string>();

  /** Les dossiers dont l'utilisateur a inversé l'état par défaut. */
  readonly #bascules = signal<ReadonlySet<string>>(new Set());

  readonly #arbre = computed(() => construire(this.fichiers()));

  protected readonly rangees = computed<Rangee[]>(() => {
    const bascules = this.#bascules();
    const defaut = this.repliParDefaut();
    const rangees: Rangee[] = [];
    const parcourir = (noeuds: Noeud[], profondeur: number) => {
      for (const n of noeuds) {
        const replie = !n.fichier && (defaut && !n.modifie) !== bascules.has(n.chemin);
        rangees.push({ nom: n.nom, chemin: n.chemin, profondeur, fichier: n.fichier, replie });
        if (!n.fichier && !replie) parcourir(n.enfants, profondeur + 1);
      }
    };
    parcourir(this.#arbre(), 0);
    return rangees;
  });

  protected readonly lettres = LETTRES;

  protected basculer(chemin: string): void {
    this.#bascules.update((s) => {
      const suite = new Set(s);
      if (!suite.delete(chemin)) suite.add(chemin);
      return suite;
    });
  }
}

/** Le nœud en construction : les enfants indexés par nom, pour qu'un dépôt de 20 000 fichiers reste linéaire. */
interface Brouillon {
  readonly nom: string;
  readonly chemin: string;
  readonly dossiers: Map<string, Brouillon>;
  readonly feuilles: EntreeArbre[];
}

function construire(fichiers: readonly EntreeArbre[]): Noeud[] {
  const racine: Brouillon = { nom: '', chemin: '', dossiers: new Map(), feuilles: [] };
  for (const f of fichiers) {
    const parties = f.chemin.split('/');
    let courant = racine;
    for (let i = 0; i < parties.length - 1; i++) {
      let suivant = courant.dossiers.get(parties[i]);
      if (!suivant) {
        suivant = { nom: parties[i], chemin: parties.slice(0, i + 1).join('/'), dossiers: new Map(), feuilles: [] };
        courant.dossiers.set(parties[i], suivant);
      }
      courant = suivant;
    }
    courant.feuilles.push(f);
  }
  return enfants(racine);
}

/** Les enfants d'un dossier, compactés : les dossiers d'abord, puis l'ordre alphabétique. */
function enfants(b: Brouillon): Noeud[] {
  const dossiers = [...b.dossiers.values()].map(compacter).sort((x, y) => x.nom.localeCompare(y.nom));
  const feuilles = b.feuilles
    .map((f) => ({ nom: f.chemin.slice(b.chemin ? b.chemin.length + 1 : 0), chemin: f.chemin, enfants: [], fichier: f, modifie: f.statut !== null }))
    .sort((x, y) => x.nom.localeCompare(y.nom));
  return [...dossiers, ...feuilles];
}

function compacter(b: Brouillon): Noeud {
  let courant = b;
  let nom = b.nom;
  while (courant.feuilles.length === 0 && courant.dossiers.size === 1) {
    courant = courant.dossiers.values().next().value!;
    nom = `${nom}/${courant.nom}`;
  }
  const contenu = enfants(courant);
  return { nom, chemin: courant.chemin, enfants: contenu, fichier: null, modifie: contenu.some((n) => n.modifie) };
}

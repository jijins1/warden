import { ChangeDetectionStrategy, Component, computed, input } from '@angular/core';
import { colorer, grammairePour } from '../coloration/coloration';
import { FichierDiff } from '../parser-diff/parser-diff';

/**
 * Un fichier du diff : son chemin, son statut, et ses blocs numérotés des deux
 * côtés, colorés d'après le langage du fichier. Rien de la plomberie git — ni
 * `index`, ni `---`/`+++`, ni plages `@@`.
 *
 * Le contenu vient d'un dépôt audité : il est rendu par interpolation, jamais
 * en HTML.
 */
@Component({
  selector: 'w-detail-fichier',
  templateUrl: './detail-fichier.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class DetailFichier {
  readonly fichier = input.required<FichierDiff>();

  protected readonly blocs = computed(() => {
    const f = this.fichier();
    const grammaire = grammairePour(f.chemin);
    return f.blocs.map((bloc) => ({
      section: bloc.section,
      lignes: bloc.lignes.map((l) => ({
        ...l,
        fragments: l.genre === 'note' ? [{ texte: l.texte, genre: null }] : colorer(l.texte, grammaire),
      })),
    }));
  });
}

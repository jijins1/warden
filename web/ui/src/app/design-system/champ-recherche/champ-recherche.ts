import {
  ChangeDetectionStrategy,
  Component,
  DestroyRef,
  ElementRef,
  computed,
  inject,
  input,
  output,
  viewChild,
} from '@angular/core';

/** Le temps de laisser finir un mot avant de relancer la recherche. */
const DELAI_MS = 250;

/** Vrai quand la frappe vise déjà un champ : `/` y est un caractère, pas un raccourci. */
function dansUnChamp(cible: EventTarget | null): boolean {
  if (!(cible instanceof HTMLElement)) {
    return false;
  }
  return cible.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(cible.tagName);
}

/**
 * Le champ de recherche d'une barre d'outils (`w-search`), avec le raccourci `/`.
 *
 * Il émet le texte cherché, ou `null` quand il est vide, une fois la frappe
 * posée — pas à chaque touche. Il ne garde pas la recherche active : elle lui
 * revient en input, pour que celui qui la range (souvent l'URL) reste la seule
 * source.
 */
@Component({
  selector: 'w-champ-recherche',
  templateUrl: './champ-recherche.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: { style: 'display: contents', '(document:keydown)': 'raccourci($event)' },
})
export class ChampRecherche {
  readonly valeur = input<string | null>(null);
  /** Ce qu'on peut chercher : « Search machine name… ». */
  readonly placeholder = input.required<string>();
  /** Le nom du champ pour un lecteur d'écran : « Search machines ». */
  readonly libelle = input.required<string>();

  readonly recherche = output<string | null>();

  protected readonly texte = computed(() => this.valeur() ?? '');

  private readonly champ = viewChild.required<ElementRef<HTMLInputElement>>('champ');
  #minuterie: ReturnType<typeof setTimeout> | undefined;

  constructor() {
    inject(DestroyRef).onDestroy(() => clearTimeout(this.#minuterie));
  }

  protected saisir(texte: string): void {
    clearTimeout(this.#minuterie);
    this.#minuterie = setTimeout(() => this.valider(texte), DELAI_MS);
  }

  /** Entrée n'attend pas le délai. */
  protected valider(texte: string): void {
    clearTimeout(this.#minuterie);
    this.recherche.emit(texte.trim() || null);
  }

  protected raccourci(evenement: KeyboardEvent): void {
    if (
      evenement.key !== '/' ||
      evenement.ctrlKey ||
      evenement.metaKey ||
      evenement.altKey ||
      dansUnChamp(evenement.target)
    ) {
      return;
    }
    evenement.preventDefault();
    this.champ().nativeElement.focus();
  }
}

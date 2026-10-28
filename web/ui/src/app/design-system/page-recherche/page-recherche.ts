import { ChangeDetectionStrategy, Component, Directive } from '@angular/core';

/**
 * Le modèle de page de recherche : titre, barre de recherche et de filtres,
 * filtres actifs, résultats, pagination. L'ordre est fixe ; l'écran qui s'en
 * sert ne remplit que les emplacements.
 *
 * Les états — chargement (`w-progress`), aucun résultat (`w-empty`), erreur
 * (`w-callout--error`) — vont dans l'emplacement des résultats.
 */
@Component({
  selector: 'w-page-recherche',
  templateUrl: './page-recherche.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class PageRecherche {}

/** Le titre de page, d'un mot : « Runs ». */
@Directive({ selector: 'w-page-recherche-titre', host: { style: 'display: contents' } })
export class PageRechercheTitre {}

/** La phrase qui dit le périmètre de la liste. */
@Directive({ selector: 'w-page-recherche-perimetre', host: { style: 'display: contents' } })
export class PageRecherchePerimetre {}

/** Au plus un bouton `primary`, à droite du titre. */
@Directive({ selector: 'w-page-recherche-action', host: { style: 'display: contents' } })
export class PageRechercheAction {}

/** La barre : recherche, `toggle`, segments, puis le compteur. */
@Directive({ selector: 'w-page-recherche-outils', host: { style: 'display: contents' } })
export class PageRechercheOutils {}

/** Les filtres actifs (`w-tags`), quand il y en a. */
@Directive({ selector: 'w-page-recherche-filtres', host: { style: 'display: contents' } })
export class PageRechercheFiltres {}

/** La table de résultats, ou l'état qui la remplace. */
@Directive({ selector: 'w-page-recherche-resultats', host: { style: 'display: contents' } })
export class PageRechercheResultats {}

/** `w-pagination`, sous les résultats. */
@Directive({ selector: 'w-page-recherche-pagination', host: { style: 'display: contents' } })
export class PageRecherchePagination {}

export const PAGE_RECHERCHE = [
  PageRecherche,
  PageRechercheTitre,
  PageRecherchePerimetre,
  PageRechercheAction,
  PageRechercheOutils,
  PageRechercheFiltres,
  PageRechercheResultats,
  PageRecherchePagination,
] as const;

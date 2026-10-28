import { ChangeDetectionStrategy, Component, Directive } from '@angular/core';

/**
 * Le modèle de page de détail : retour, en-tête (titre, contexte, actions,
 * état à l'extrême droite), puis le corps — encarts, faits, sections empilées.
 *
 * Pas d'onglets : tout se lit en défilant, et la page se cherche avec Ctrl+F.
 */
@Component({
  selector: 'w-page-detail',
  templateUrl: './page-detail.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class PageDetail {}

/** Le lien `w-back` vers la liste d'origine, avec son nom. */
@Directive({ selector: 'w-page-detail-retour', host: { style: 'display: contents' } })
export class PageDetailRetour {}

/** Le titre : le playbook, le nom d'hôte. */
@Directive({ selector: 'w-page-detail-titre', host: { style: 'display: contents' } })
export class PageDetailTitre {}

/** Après le titre : « on » et l'`EnvBadge`. */
@Directive({ selector: 'w-page-detail-contexte', host: { style: 'display: contents' } })
export class PageDetailContexte {}

/** Les actions en `outline` ; jamais de `primary` ici. */
@Directive({ selector: 'w-page-detail-actions', host: { style: 'display: contents' } })
export class PageDetailActions {}

/** Le `StateChip`, toujours en dernier, à l'extrême droite. */
@Directive({ selector: 'w-page-detail-etat', host: { style: 'display: contents' } })
export class PageDetailEtat {}

/** Encarts, faits, puis sections, dans cet ordre. */
@Directive({ selector: 'w-page-detail-corps', host: { style: 'display: contents' } })
export class PageDetailCorps {}

export const PAGE_DETAIL = [
  PageDetail,
  PageDetailRetour,
  PageDetailTitre,
  PageDetailContexte,
  PageDetailActions,
  PageDetailEtat,
  PageDetailCorps,
] as const;

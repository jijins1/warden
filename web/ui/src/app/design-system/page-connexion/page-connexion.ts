import { ChangeDetectionStrategy, Component, Directive } from '@angular/core';

/**
 * Le modèle de page de connexion : une carte de 360px centrée sur `bg`, sans
 * barre de navigation. La marque y prend le carré `primary`, puisqu'il n'y a
 * pas de bandeau pour porter `masthead-accent`.
 */
@Component({
  selector: 'w-page-connexion',
  templateUrl: './page-connexion.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class PageConnexion {}

/** La phrase sous la marque : « Sign in to read your runs. » */
@Directive({ selector: 'w-page-connexion-introduction', host: { style: 'display: contents' } })
export class PageConnexionIntroduction {}

/** L'éventuel encart, les champs et le bouton « Sign in ». */
@Directive({ selector: 'w-page-connexion-contenu', host: { style: 'display: contents' } })
export class PageConnexionContenu {}

export const PAGE_CONNEXION = [PageConnexion, PageConnexionIntroduction, PageConnexionContenu] as const;

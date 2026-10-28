import { ChangeDetectionStrategy, Component, Directive } from '@angular/core';

/**
 * La barre de navigation : un bandeau graphite de 48px, identique dans les
 * deux thèmes, qui donne son cadre à l'app.
 *
 * Un template : il porte la structure (marque, liens, puis les actions à
 * droite) et l'appelant projette le contenu. Les liens portent `is-active` ;
 * aucune autre couleur que celles du bandeau.
 */
@Component({
  selector: 'w-masthead',
  templateUrl: './masthead.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class Masthead {}

/** La marque : `<a class="w-wordmark">warden</a>`. */
@Directive({ selector: 'w-masthead-marque', host: { style: 'display: contents' } })
export class MastheadMarque {}

/** Les liens de navigation, chacun un `<a>` qui prend `is-active`. */
@Directive({ selector: 'w-masthead-nav', host: { style: 'display: contents' } })
export class MastheadNav {}

/** Thème et utilisateur, à droite (`w-iconbtn`, `w-user`). */
@Directive({ selector: 'w-masthead-actions', host: { style: 'display: contents' } })
export class MastheadActions {}

export const MASTHEAD = [Masthead, MastheadMarque, MastheadNav, MastheadActions] as const;

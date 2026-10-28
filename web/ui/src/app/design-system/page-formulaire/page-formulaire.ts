import { ChangeDetectionStrategy, Component, Directive } from '@angular/core';

/**
 * Le modèle de page de formulaire : retour, puis une seule carte de 640px au
 * plus qui contient le titre, la phrase d'introduction et tout le formulaire.
 *
 * Il sert au lancement d'un run, au changement de mot de passe, et à tout
 * formulaire d'une page.
 */
@Component({
  selector: 'w-page-formulaire',
  templateUrl: './page-formulaire.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class PageFormulaire {}

/** Le lien `w-back`. */
@Directive({ selector: 'w-page-formulaire-retour', host: { style: 'display: contents' } })
export class PageFormulaireRetour {}

/** Un verbe à l'infinitif : « Launch a run ». */
@Directive({ selector: 'w-page-formulaire-titre', host: { style: 'display: contents' } })
export class PageFormulaireTitre {}

/** Ce que l'action fait et ce qu'elle exige, en une phrase. */
@Directive({ selector: 'w-page-formulaire-introduction', host: { style: 'display: contents' } })
export class PageFormulaireIntroduction {}

/** Le formulaire : champs, erreur serveur, puis `w-form-actions`. */
@Directive({ selector: 'w-page-formulaire-contenu', host: { style: 'display: contents' } })
export class PageFormulaireContenu {}

export const PAGE_FORMULAIRE = [
  PageFormulaire,
  PageFormulaireRetour,
  PageFormulaireTitre,
  PageFormulaireIntroduction,
  PageFormulaireContenu,
] as const;

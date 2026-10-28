import { ChangeDetectionStrategy, Component, input } from '@angular/core';
import { RouterLink } from '@angular/router';
import { MatIconModule } from '@angular/material/icon';
import { PAGE_DETAIL } from '../../../design-system/page-detail/page-detail';
import { OutilsMachine } from './outils-machine/outils-machine';
import { OngletsMachine } from './onglets-machine/onglets-machine';
import { PlaybooksMachine } from './playbooks-machine/playbooks-machine';
import { RunsMachine } from './runs-machine/runs-machine';

/**
 * L'écran d'une machine : quels playbooks y ont tourné (§6.5 dédupliqué, vue
 * par défaut), et en seconde vue tout ce qui l'a touchée run par run. On y
 * arrive depuis la liste des machines (`/hosts`), ou par un lien profond.
 *
 * Restreint à ce que l'appelant peut voir — le sien, ou tout run couvert par
 * un droit d'environnement vivant (Policy.CanViewRun) — et non la totalité de
 * l'historique de la machine : un environnement hors de ses droits reste hors
 * de portée. Ce n'est donc pas une promesse d'exhaustivité, et sur un outil
 * d'audit, croire avoir tout vu est pire que savoir qu'on n'a qu'une part.
 *
 * La page ne charge rien : elle lit la machine et la fenêtre dans l'URL et les
 * passe aux deux blocs, qui chargent chacun leurs données.
 */
@Component({
  selector: 'w-machine',
  imports: [RouterLink, MatIconModule, PAGE_DETAIL, OutilsMachine, OngletsMachine, PlaybooksMachine, RunsMachine],
  templateUrl: './machine.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class Machine {
  /** Lié depuis la route `/hosts/:host` par `withComponentInputBinding`. */
  readonly host = input.required<string>();
  /** Query params de la fenêtre, liés de la même façon. */
  readonly since = input<string>();
  readonly until = input<string>();
}

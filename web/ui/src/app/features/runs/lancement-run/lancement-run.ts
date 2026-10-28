import { ChangeDetectionStrategy, Component, computed, input } from '@angular/core';
import { RouterLink } from '@angular/router';
import { MatIconModule } from '@angular/material/icon';
import { PAGE_FORMULAIRE } from '../../../design-system/page-formulaire/page-formulaire';
import { EnvoiLancement } from './envoi-lancement/envoi-lancement';
import { PreremplissageLancement } from './envoi-lancement/formulaire-lancement/formulaire-lancement';

/**
 * La page `/runs/new` : lance un run sur la branche protégée d'un dépôt, depuis
 * le navigateur (ADR-0042).
 *
 * PAS derrière `adminGuard`, contrairement à la liste des dépôts : lancer un
 * run demande un droit d'accès sur l'ENVIRONNEMENT (la même autorisation que
 * `warden submit`), pas un droit d'administration sur le dépôt. Un utilisateur
 * sans droit d'administration mais avec un accès `prod` doit pouvoir l'ouvrir.
 *
 * Le dépôt et l'environnement sont des champs de formulaire, préremplis depuis
 * les paramètres de requête quand ils sont connus (lien « Launch a run » sur
 * la liste des dépôts, ou « Relaunch » sur le détail d'un run) : rien
 * n'énumère les dépôts pour un utilisateur ordinaire, mais il connaît déjà les
 * siens par son propre historique de runs. Les inputs sont liés à ces
 * paramètres par `withComponentInputBinding`.
 */
@Component({
  selector: 'w-lancement-run',
  imports: [RouterLink, MatIconModule, PAGE_FORMULAIRE, EnvoiLancement],
  templateUrl: './lancement-run.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class LancementRun {
  readonly repo = input<string>();
  readonly environment = input<string>();
  readonly inventory = input<string>();
  readonly playbook = input<string>();

  protected readonly valeurInitiale = computed<PreremplissageLancement>(() => ({
    repo: this.repo() ?? '',
    environment: this.environment() ?? '',
    inventory: this.inventory() ?? '',
    playbook: this.playbook() ?? '',
  }));
}

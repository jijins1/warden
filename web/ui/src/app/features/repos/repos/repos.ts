import { ChangeDetectionStrategy, Component } from '@angular/core';
import { PAGE_RECHERCHE } from '../../../design-system/page-recherche/page-recherche';
import { ListeRepos } from './liste-repos/liste-repos';

/**
 * Les dépôts configurés.
 *
 * `GET /admin/repos` rend TOUTE la configuration — une carte de l'organisation,
 * pas une vue par utilisateur (voir le commentaire de `HandleListRepos`). C'est
 * pourquoi cet écran est derrière `adminGuard`, contrairement à « Runs ».
 */
@Component({
  selector: 'w-repos',
  imports: [PAGE_RECHERCHE, ListeRepos],
  templateUrl: './repos.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class Repos {}

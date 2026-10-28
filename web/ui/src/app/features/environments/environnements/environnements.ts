import { ChangeDetectionStrategy, Component } from '@angular/core';
import { PAGE_RECHERCHE } from '../../../design-system/page-recherche/page-recherche';
import { ListeEnvironnements } from './liste-environnements/liste-environnements';

/**
 * Les environnements connus, vus par leur clé d'accès.
 *
 * `GET /admin/keys` rend l'inventaire de clés — c'est ce qui fait qu'un
 * environnement EXISTE pour warden : sans clé, il n'y a rien à administrer à
 * son sujet (ADR-0020). La partie privée n'apparaît jamais ici, comme nulle
 * part ailleurs dans le produit.
 */
@Component({
  selector: 'w-environnements',
  imports: [PAGE_RECHERCHE, ListeEnvironnements],
  templateUrl: './environnements.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class Environnements {}

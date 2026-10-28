import { ChangeDetectionStrategy, Component, input } from '@angular/core';
import { DatePipe } from '@angular/common';
import { RouterLink } from '@angular/router';
import { BadgeEnvironnement } from '../../../../../badge-environnement/badge-environnement';

/** Une machine enrôlée, prête à afficher. */
export interface LigneMachine {
  /** `environnement/host` : un même nom peut vivre dans deux environnements. */
  readonly cle: string;
  readonly host: string;
  readonly environnement: string;
  /** `adresse:port`, le port omis quand c'est 22. */
  readonly adresse: string;
  readonly login: string;
  /** « Verified » ou « Declared » : un mode d'enrôlement, pas un état. */
  readonly enrolement: string;
  readonly enroleeLe: string;
}

/**
 * La table des machines, en mode simple de `DataTable` : chaque ligne est un
 * lien vers la machine, et rien d'autre n'y est cliquable.
 */
@Component({
  selector: 'w-table-machines',
  imports: [DatePipe, RouterLink, BadgeEnvironnement],
  templateUrl: './table-machines.html',
  styleUrl: './table-machines.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class TableMachines {
  readonly lignes = input.required<readonly LigneMachine[]>();
}

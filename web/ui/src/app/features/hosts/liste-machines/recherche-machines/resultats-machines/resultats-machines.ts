import { ChangeDetectionStrategy, Component, computed, input, output } from '@angular/core';
import { MatIconModule } from '@angular/material/icon';
import { LigneMachine, TableMachines } from './table-machines/table-machines';

/** Où en est la liste : ce qui est reçu, et ce qui se passe. */
export interface EtatResultatsMachines {
  lignes: readonly LigneMachine[];
  chargement: boolean;
  /** Faux tant que la première page n'a pas répondu : distingue « vide » de « pas encore ». */
  charge: boolean;
  erreur: string | null;
}

/** Les machines, ou l'état qui les remplace : chargement, erreur, aucune machine. */
@Component({
  selector: 'w-resultats-machines',
  imports: [MatIconModule, TableMachines],
  templateUrl: './resultats-machines.html',
  host: { style: 'display: contents' },
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ResultatsMachines {
  readonly etat = input.required<EtatResultatsMachines>();
  readonly filtrePose = input(false);

  readonly nouvelEssai = output<void>();
  readonly effacement = output<void>();

  protected readonly lignes = computed(() => this.etat().lignes);
  protected readonly erreur = computed(() => this.etat().erreur);
  protected readonly avecLignes = computed(() => this.lignes().length > 0);
  /** La liste est vidée à chaque rechargement : sans ligne, un chargement est un premier chargement. */
  protected readonly premierChargement = computed(() => this.etat().chargement && !this.avecLignes());
  protected readonly vide = computed(() => {
    const e = this.etat();
    return e.charge && !e.chargement && !e.erreur && !this.avecLignes();
  });
}

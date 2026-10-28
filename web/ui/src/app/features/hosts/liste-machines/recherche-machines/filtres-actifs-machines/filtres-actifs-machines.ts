import { ChangeDetectionStrategy, Component, computed, input, output } from '@angular/core';
import { FiltreMachines } from '../../../../../core/services/machines/machines.service';
import type { PatchFiltreMachines } from '../recherche-machines';

/** Un filtre posé, tel que la rangée de tags le montre. */
interface EtiquetteFiltre {
  cle: keyof FiltreMachines;
  /** `clé:valeur`, en mono : la forme qu'on recopierait dans une URL. */
  libelle: string;
}

/**
 * Les filtres posés, en tags retirables, et « Clear filters ».
 *
 * Rien du tout quand aucun filtre n'est posé.
 */
@Component({
  selector: 'w-filtres-actifs-machines',
  templateUrl: './filtres-actifs-machines.html',
  host: { style: 'display: contents' },
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class FiltresActifsMachines {
  readonly filtre = input.required<FiltreMachines>();

  readonly changement = output<PatchFiltreMachines>();
  readonly effacement = output<void>();

  protected readonly etiquettes = computed<EtiquetteFiltre[]>(() => {
    const { env, q } = this.filtre();
    const etiquettes: EtiquetteFiltre[] = [];
    if (env !== undefined) etiquettes.push({ cle: 'env', libelle: `env:${env}` });
    if (q !== undefined) etiquettes.push({ cle: 'q', libelle: `q:${q}` });
    return etiquettes;
  });
  protected readonly visible = computed(() => this.etiquettes().length > 0);

  protected retirer(cle: keyof FiltreMachines): void {
    this.changement.emit({ [cle]: null });
  }
}

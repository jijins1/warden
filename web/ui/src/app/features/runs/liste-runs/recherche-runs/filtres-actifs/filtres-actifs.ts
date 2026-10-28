import { ChangeDetectionStrategy, Component, computed, input, output } from '@angular/core';
import { FiltreRuns } from '../../../../../core/services/runs/runs.service';
import type { PatchFiltre } from '../recherche-runs';

/** Un filtre posé, tel que la rangée de tags le montre. */
export interface EtiquetteFiltre {
  cle: keyof FiltreRuns;
  /** `clé:valeur`, en mono : la forme qu'on recopierait dans une URL. */
  libelle: string;
}

/** Le libellé d'un filtre posé ; `null` pour un filtre absent. */
function libeller(cle: keyof FiltreRuns, filtre: FiltreRuns): string | null {
  const valeur = filtre[cle];
  if (valeur === undefined) {
    return null;
  }
  if (cle === 'changed_min') {
    return `changed>${Number(valeur) - 1}`;
  }
  if (cle === 'mine') {
    return 'mine';
  }
  return `${cle}:${valeur}`;
}

/**
 * Les filtres posés, en tags retirables, et « Clear filters ».
 *
 * Rien du tout quand aucun filtre n'est posé.
 */
@Component({
  selector: 'w-filtres-actifs',
  templateUrl: './filtres-actifs.html',
  host: { style: 'display: contents' },
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class FiltresActifs {
  readonly filtre = input.required<FiltreRuns>();

  readonly changement = output<PatchFiltre>();
  readonly effacement = output<void>();

  protected readonly etiquettes = computed<EtiquetteFiltre[]>(() => {
    const filtre = this.filtre();
    return (Object.keys(filtre) as (keyof FiltreRuns)[]).flatMap((cle) => {
      const libelle = libeller(cle, filtre);
      return libelle === null ? [] : [{ cle, libelle }];
    });
  });
  protected readonly visible = computed(() => this.etiquettes().length > 0);

  protected retirer(cle: keyof FiltreRuns): void {
    this.changement.emit({ [cle]: null });
  }
}

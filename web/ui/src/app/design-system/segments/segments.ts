import { ChangeDetectionStrategy, Component, input, output } from '@angular/core';

/**
 * Un choix exclusif en rangée de boutons accolés (`w-seg`).
 *
 * Recliquer l'option active la désélectionne : le composant émet alors `null`.
 * Il ne garde pas l'option active lui-même — elle lui revient en input, pour
 * que celui qui la range (souvent l'URL) reste la seule source.
 */
@Component({
  selector: 'w-segments',
  templateUrl: './segments.html',
  host: { style: 'display: contents' },
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class Segments {
  readonly options = input.required<readonly string[]>();
  readonly actif = input<string | null>(null);
  /** Le nom du groupe pour un lecteur d'écran : « State », « Environment ». */
  readonly libelle = input.required<string>();

  readonly choix = output<string | null>();

  protected choisir(option: string): void {
    this.choix.emit(option === this.actif() ? null : option);
  }
}

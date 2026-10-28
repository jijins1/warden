import { ChangeDetectionStrategy, Component, input, signal } from '@angular/core';
import { ClipboardModule } from '@angular/cdk/clipboard';

/**
 * Un texte à copier tel quel : un bloc `!vault |`, une commande, une clé.
 *
 * Sans retour à la ligne automatique — un bloc copiable ne se reformate jamais —
 * et affiché par interpolation, jamais en HTML. Le bouton dit « Copied » 1,6 s,
 * puis revient.
 */
@Component({
  selector: 'w-copy-block',
  imports: [ClipboardModule],
  templateUrl: './copy-block.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class CopyBlock {
  readonly texte = input.required<string>();

  protected readonly copie = signal(false);
  #minuterie: ReturnType<typeof setTimeout> | undefined;

  protected copier(reussi: boolean): void {
    clearTimeout(this.#minuterie);
    this.copie.set(reussi);
    this.#minuterie = setTimeout(() => this.copie.set(false), 1600);
  }
}

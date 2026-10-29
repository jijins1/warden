import { ChangeDetectionStrategy, Component, computed, input } from '@angular/core';
import { MatIconModule } from '@angular/material/icon';
import { avecTags, lireFlags } from '../flags-lus/flags-lus';

/**
 * Les tags avec lesquels un run a tourné, mis en avant.
 *
 * À part des autres drapeaux, et AVANT les faits du run : `--tags` et
 * `--skip-tags` changent CE QUI s'est exécuté. Un run lancé avec `--tags deploy`
 * n'a joué qu'une partie du playbook, et le lire au milieu d'une rangée de
 * drapeaux laisserait croire qu'il l'a joué en entier.
 *
 * Rien quand le run n'en avait pas : le playbook a tourné tel quel, il n'y a
 * rien à signaler.
 */
@Component({
  selector: 'w-tags-run',
  imports: [MatIconModule],
  templateUrl: './tags-run.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: { style: 'display: contents' },
})
export class TagsRun {
  readonly flags = input<readonly string[]>([]);

  protected readonly lus = computed(() => lireFlags(this.flags()));
  protected readonly avecTags = computed(() => avecTags(this.lus()));
}

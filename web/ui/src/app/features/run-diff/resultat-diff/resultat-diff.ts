import { ChangeDetectionStrategy, Component, computed, input } from '@angular/core';
import { MatIconModule } from '@angular/material/icon';
import { UnifiedDiff } from '../../../design-system/unified-diff/unified-diff';

/** Le diff calculé, ou l'annonce que les deux arbres coïncident. */
@Component({
  selector: 'w-resultat-diff',
  imports: [MatIconModule, UnifiedDiff],
  templateUrl: './resultat-diff.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ResultatDiff {
  readonly texte = input.required<string>();

  protected readonly identiques = computed(() => this.texte() === '');
}

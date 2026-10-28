import { ChangeDetectionStrategy, Component, computed, input } from '@angular/core';

/** Une ligne de diff unifié, classée pour l'affichage : la classe `w-diff` qui la peint. */
interface LigneDiff {
  readonly texte: string;
  readonly genre: 'add' | 'del' | 'hunk' | 'file' | 'ctx';
}

/**
 * Un diff unifié, ligne à ligne, dans un `w-diff` : ajouts en `ok`, retraits en
 * `failed`, en-têtes de bloc en `skipped`, contexte en `text-dim`.
 *
 * Le contenu vient d'un dépôt ou d'une machine auditée et n'est pas de
 * confiance : il est rendu par interpolation, jamais en HTML, sans quoi il
 * pourrait injecter du balisage dans la page d'audit.
 */
@Component({
  selector: 'w-unified-diff',
  template: `<pre class="w-diff"><code>@for (ligne of lignes(); track $index) {<span [class]="ligne.genre">{{ ligne.texte }}</span>}</code></pre>`,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class UnifiedDiff {
  readonly texte = input.required<string>();

  protected readonly lignes = computed<LigneDiff[]>(() =>
    this.texte()
      .split('\n')
      .map((texte) => ({ texte, genre: classer(texte) })),
  );
}

function classer(ligne: string): LigneDiff['genre'] {
  if (ligne.startsWith('@@')) return 'hunk';
  // L'ordre compte : `+++` et `---` sont des en-têtes de fichier, pas des
  // modifications. Les tester APRÈS `+`/`-` les colorerait en vert et rouge.
  if (ligne.startsWith('+++') || ligne.startsWith('---')) return 'file';
  if (ligne.startsWith('diff --git') || ligne.startsWith('index ')) return 'file';
  if (ligne.startsWith('new file') || ligne.startsWith('deleted file')) return 'file';
  if (ligne.startsWith('+')) return 'add';
  if (ligne.startsWith('-')) return 'del';
  return 'ctx';
}

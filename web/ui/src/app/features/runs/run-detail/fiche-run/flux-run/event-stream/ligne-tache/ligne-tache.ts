import { ChangeDetectionStrategy, Component, computed, input, output } from '@angular/core';
import { MatTooltipModule } from '@angular/material/tooltip';
import { EventState } from '../../../../../../../core/models/wire';
import { StateChip } from '../../../../../../../design-system/state-chip/state-chip';
import { GroupeTache } from '../regroupement/regroupement';

/** Fige l'ordre des compteurs dans le résumé d'une tâche : `changed` en tête,
 * c'est la métrique d'audit principale du produit (ADR-0008). */
const ORDRE_COMPTES: EventState[] = ['changed', 'ok', 'failed', 'unreachable', 'skipped'];

/**
 * L'état qu'affiche le chip d'une tâche : le plus grave qu'elle ait connu. Un
 * seul hôte en échec fait d'une tâche une tâche en échec, quel que soit le
 * nombre d'hôtes passés sans encombre.
 */
const GRAVITE: EventState[] = ['failed', 'unreachable', 'changed', 'ok', 'skipped'];

/**
 * Le résumé repliable d'une tâche : son nom, ses comptes par état, son état.
 * Une tâche en échec prend `is-failed`.
 */
@Component({
  selector: 'w-ligne-tache',
  imports: [MatTooltipModule, StateChip],
  templateUrl: './ligne-tache.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: { style: 'display: block' },
})
export class LigneTache {
  readonly groupe = input.required<GroupeTache>();
  readonly deplie = input.required<boolean>();

  readonly basculer = output<void>();

  /** Le titre d'une tâche, rôle en préfixe s'il existe — la même règle que la CLI. */
  protected readonly titre = computed(() => {
    const g = this.groupe();
    if (!g.taskName) {
      return '(tâche sans nom)';
    }
    return g.role ? `${g.role} : ${g.taskName}` : g.taskName;
  });

  /** Les comptes de la tâche, dans l'ordre fixé par `ORDRE_COMPTES` : « changed=2 ok=1 ». */
  protected readonly comptes = computed(() => {
    const c = this.groupe().comptes;
    return ORDRE_COMPTES.filter((etat) => (c[etat] ?? 0) > 0)
      .map((etat) => `${etat}=${c[etat]}`)
      .join(' ');
  });

  protected readonly etat = computed(() => {
    const c = this.groupe().comptes;
    return GRAVITE.find((etat) => (c[etat] ?? 0) > 0) ?? null;
  });

  protected readonly enEchec = computed(() => this.groupe().echecs.length > 0);
}

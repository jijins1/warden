import { ChangeDetectionStrategy, Component, computed, input } from '@angular/core';
/**
 * Les huit états que le chip sait dire : ceux d'un event (casse d'Ansible) et
 * ceux d'un run. Déclarés ici et non importés du modèle d'API : le design system
 * ne connaît aucune feature, et le compilateur vérifie quand même qu'un
 * `RunState` ou un `EventState` s'y range.
 */
export type EtatAffichable =
  | 'ok'
  | 'changed'
  | 'failed'
  | 'unreachable'
  | 'skipped'
  | 'unknown'
  | 'running'
  | 'succeeded'
  | 'interrupted';

/**
 * LA source unique de la couleur d'état. Ne jamais colorer un état à la main.
 *
 * Trois règles y sont tenues, et aucune n'est cosmétique :
 *
 *  1. icône, mot ET couleur, toujours les trois. La couleur seule échoue pour
 *     un lecteur daltonien et échoue encore à faible contraste ; sur un produit
 *     dont le seul métier est de rapporter fidèlement ce qui s'est passé, ce
 *     n'est pas un défaut d'esthétique ;
 *  2. `interrupted` ne s'affiche JAMAIS seul. « Interrompu » sans la cause est
 *     presque inutile (core/run.go) : un Ctrl-C est un signal fort — quelqu'un
 *     a vu quelque chose de travers — là où un `client_disconnect_timeout` est
 *     accidentel. État et cause sont donc un seul composant ;
 *  3. `interrupted` est ARDOISE et jamais rouge. L'ADR-0031 pose qu'un runner
 *     mourant ne prononce pas de verdict : le peindre en échec dirait « ton
 *     playbook a échoué » d'un playbook qui n'a rien dit.
 */
@Component({
  selector: 'w-state-chip',
  templateUrl: './state-chip.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: { class: 'w-chip-host' },
})
export class StateChip {
  readonly state = input.required<EtatAffichable>();

  /** Exigée par le domaine si et seulement si `state` vaut `interrupted`. */
  readonly cause = input<string | undefined>();

  protected readonly jeton = computed(() => JETONS[this.state()]);

  protected readonly libelle = computed(() => LIBELLES[this.state()]);

  /**
   * La cause à afficher à côté de l'état.
   *
   * Le schéma garantit l'équivalence `interrupted` ⟺ cause renseignée (CHECK du
   * 00001). Une cause manquante est donc un défaut du client, pas un cas
   * métier : il est RENDU VISIBLE plutôt que masqué, parce qu'un chip muet
   * ferait exactement le tort que la règle 2 interdit.
   */
  protected readonly causeAffichee = computed(() => {
    if (this.state() !== 'interrupted') {
      return null;
    }
    return this.cause() || 'cause missing';
  });

  /** `user_interrupt` est le signal fort : quelqu'un a vu quelque chose. */
  protected readonly causeForte = computed(() => this.cause() === 'user_interrupt');
}

/** État → triplet de jetons CSS (`--w-ok`, `--w-ok-bg`, `--w-ok-br`), lu par `.w-chip[data-state]`. */
const JETONS: Record<EtatAffichable, string> = {
  ok: 'ok',
  succeeded: 'ok',
  changed: 'changed',
  failed: 'failed',
  unreachable: 'unreachable',
  skipped: 'skipped',
  unknown: 'unknown',
  running: 'running',
  interrupted: 'interrupted',
};

/**
 * Les états d'EVENT gardent la casse d'Ansible, les états de RUN sont
 * capitalisés. Ce n'est pas une inconséquence : ce sont deux vocabulaires
 * distincts, et les afficher pareil laisserait croire à un seul.
 */
const LIBELLES: Record<EtatAffichable, string> = {
  ok: 'ok',
  changed: 'changed',
  failed: 'failed',
  unreachable: 'unreachable',
  skipped: 'skipped',
  unknown: 'unknown',
  succeeded: 'Succeeded',
  running: 'Running',
  interrupted: 'Interrupted',
};

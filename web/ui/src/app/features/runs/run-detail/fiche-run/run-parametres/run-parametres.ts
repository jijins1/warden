import { ChangeDetectionStrategy, Component, computed, input } from '@angular/core';
import { avecTags, lireFlags } from '../flags-lus/flags-lus';

/**
 * Les paramètres avec lesquels un run a tourné.
 *
 * Les valeurs d'extra vars s'affichent EN CLAIR, et c'est l'audit qui le veut
 * (ADR-0039) : « quelle version a été déployée » se lit ici. Ce qui ne s'y lit
 * pas, ce sont les variables `forward_env` — leurs valeurs ne sont jamais
 * enregistrées, donc il n'y a rien à masquer : le dire évite de les chercher.
 */
@Component({
  selector: 'w-run-parametres',
  templateUrl: './run-parametres.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class RunParametres {
  readonly flags = input<readonly string[]>([]);

  protected readonly lus = computed(() => lireFlags(this.flags()));
  protected readonly sansDrapeau = computed(
    () => this.lus().options.length === 0 && this.lus().extraVars.length === 0,
  );
  protected readonly aDrapeau = computed(() => !this.sansDrapeau());
  /** Des tags et rien d'autre : ils sont montrés plus haut, la section ne doit pas dire « sans drapeau ». */
  protected readonly seulementDesTags = computed(
    () => this.sansDrapeau() && avecTags(this.lus()),
  );
}

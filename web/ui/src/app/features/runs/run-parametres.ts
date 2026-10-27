import { ChangeDetectionStrategy, Component, computed, input } from '@angular/core';

/** Une extra var telle qu'elle a été passée : `--extra-vars=nom=valeur`. */
export interface ExtraVar {
  nom: string;
  valeur: string;
}

const PREFIXE_EXTRA_VAR = '--extra-vars=';

/**
 * Sépare les drapeaux d'un run en options et en extra vars.
 *
 * Sur la forme que produit `FlagsAnsible.Composer` et que le serveur valide
 * (`core.FlagsDemandes`) : une extra var est TOUJOURS `--extra-vars=nom=valeur`,
 * en une seule chaîne. Le nom s'arrête au premier `=`, la valeur peut en
 * contenir d'autres.
 */
export function lireFlags(flags: readonly string[]): { options: string[]; extraVars: ExtraVar[] } {
  const options: string[] = [];
  const extraVars: ExtraVar[] = [];
  for (const f of flags) {
    if (f.startsWith(PREFIXE_EXTRA_VAR)) {
      const paire = f.slice(PREFIXE_EXTRA_VAR.length);
      const i = paire.indexOf('=');
      extraVars.push(
        i < 0 ? { nom: paire, valeur: '' } : { nom: paire.slice(0, i), valeur: paire.slice(i + 1) },
      );
    } else {
      options.push(f);
    }
  }
  return { options, extraVars };
}

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
  styleUrl: './run-parametres.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class RunParametres {
  readonly flags = input<readonly string[]>([]);

  protected readonly lus = computed(() => lireFlags(this.flags()));
  protected readonly sansDrapeau = computed(
    () => this.lus().options.length === 0 && this.lus().extraVars.length === 0,
  );
}

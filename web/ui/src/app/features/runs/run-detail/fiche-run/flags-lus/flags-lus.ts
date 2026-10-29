/** Une extra var telle qu'elle a été passée : `--extra-vars=nom=valeur`. */
export interface ExtraVar {
  nom: string;
  valeur: string;
}

const PREFIXE_EXTRA_VAR = '--extra-vars=';
const PREFIXE_TAGS = '--tags=';
const PREFIXE_SKIP_TAGS = '--skip-tags=';

/** Les drapeaux d'un run, rangés comme la fiche les montre. */
export interface FlagsLus {
  options: string[];
  extraVars: ExtraVar[];
  /** Les noms de `--tags`, un par tag : la virgule sépare, elle n'en fait pas partie. */
  tags: string[];
  skipTags: string[];
}

/**
 * Sépare les drapeaux d'un run en options, extra vars et tags.
 *
 * Sur la forme que produit `FlagsAnsible.Composer` et que le serveur valide
 * (`core.FlagsDemandes`) : une extra var est TOUJOURS `--extra-vars=nom=valeur`,
 * en une seule chaîne. Le nom s'arrête au premier `=`, la valeur peut en
 * contenir d'autres. Les tags sont `--tags=a,b` et `--skip-tags=a,b`, et ils
 * sortent des options : la fiche les montre à part (`TagsRun`).
 */
export function lireFlags(flags: readonly string[]): FlagsLus {
  const lus: FlagsLus = { options: [], extraVars: [], tags: [], skipTags: [] };
  for (const f of flags) {
    if (f.startsWith(PREFIXE_EXTRA_VAR)) {
      const paire = f.slice(PREFIXE_EXTRA_VAR.length);
      const i = paire.indexOf('=');
      lus.extraVars.push(
        i < 0 ? { nom: paire, valeur: '' } : { nom: paire.slice(0, i), valeur: paire.slice(i + 1) },
      );
    } else if (f.startsWith(PREFIXE_TAGS)) {
      lus.tags.push(...separerTags(f.slice(PREFIXE_TAGS.length)));
    } else if (f.startsWith(PREFIXE_SKIP_TAGS)) {
      lus.skipTags.push(...separerTags(f.slice(PREFIXE_SKIP_TAGS.length)));
    } else {
      lus.options.push(f);
    }
  }
  return lus;
}

function separerTags(valeur: string): string[] {
  return valeur.split(',').filter((t) => t !== '');
}

/** Le run n'a joué qu'une partie du playbook : il portait `--tags` ou `--skip-tags`. */
export function avecTags(lus: FlagsLus): boolean {
  return lus.tags.length > 0 || lus.skipTags.length > 0;
}

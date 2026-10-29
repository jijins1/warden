/**
 * Les options Ansible du formulaire de lancement, telles qu'il les rend.
 *
 * Un type nommé plutôt qu'inline : `composerFlags` en dépend et doit rester
 * testable seule, sans monter de composant.
 */
export interface OptionsLancement {
  check: boolean;
  diff: boolean;
  become: boolean;
  becomeUser: string;
  limit: string;
  /** `--tags` et `--skip-tags` : des noms séparés par des virgules, tels que tapés. */
  tags: string;
  skipTags: string;
  /** Le NOMBRE de `v` demandés — 0 à VERBOSITE_MAX, comme `-v` à `-vvvv`. */
  verbosity: number;
  extraVars: { cle: string; valeur: string }[];
}

/**
 * Le niveau de verbosité le plus élevé que le serveur accepte.
 *
 * Miroir manuel de `core.familleVerbosite` et de ses quatre entrées
 * (`internal/server/internal/core/flags.go`), comme les types de `wire.ts` le
 * sont de leurs structures Go. Ansible accepte `-vvvvv` et au-delà ;
 * l'allowlist s'arrête à quatre, et un cinquième `v` serait refusé en 400.
 */
export const VERBOSITE_MAX = 4;

/**
 * Compose les drapeaux tels que `core.FlagsDemandes` les attend.
 *
 * Une SEULE chaîne par drapeau, `--nom=valeur` — jamais deux tokens séparés :
 * `internal/server/internal/core/flags.go` (`ValiderFlags`) coupe sur le
 * premier `=` et refuse tout le reste. Le serveur REFUSE ce qui n'est pas dans
 * son allowlist plutôt que de le filtrer (ADR-0038), donc cette fonction n'a
 * pas à deviner ce qui est accepté — juste à produire la forme exacte que
 * l'allowlist reconnaît.
 *
 * Les lignes de `extraVars` dont la clé est vide sont ignorées : un formulaire
 * qui laisse une ligne blanche ne doit pas produire `--extra-vars==valeur`.
 */
export function composerFlags(v: OptionsLancement): string[] {
  const flags: string[] = [];
  if (v.check) flags.push('--check');
  if (v.diff) flags.push('--diff');
  if (v.become) flags.push('--become');
  if (v.become && v.becomeUser.trim()) {
    flags.push(`--become-user=${v.becomeUser.trim()}`);
  }
  // Les `v` se répètent dans UN SEUL drapeau : l'allowlist nomme `-v`, `-vv`,
  // `-vvv` et `-vvvv`, et les range sous une clé d'unicité commune — deux
  // niveaux envoyés ensemble sont refusés, pas fusionnés.
  const niveau = Math.min(Math.max(Math.trunc(v.verbosity) || 0, 0), VERBOSITE_MAX);
  if (niveau > 0) {
    flags.push(`-${'v'.repeat(niveau)}`);
  }
  if (v.limit.trim()) {
    flags.push(`--limit=${v.limit.trim()}`);
  }
  // Les espaces autour des virgules sont retirés : `deploy, config` se tape
  // naturellement, et le serveur refuse un espace dans la valeur.
  const tags = normaliserTags(v.tags);
  if (tags) {
    flags.push(`--tags=${tags}`);
  }
  const skipTags = normaliserTags(v.skipTags);
  if (skipTags) {
    flags.push(`--skip-tags=${skipTags}`);
  }
  for (const { cle, valeur } of v.extraVars) {
    if (cle.trim()) {
      flags.push(`--extra-vars=${cle.trim()}=${valeur}`);
    }
  }
  return flags;
}

function normaliserTags(valeur: string): string {
  return valeur
    .split(',')
    .map((t) => t.trim())
    .filter((t) => t !== '')
    .join(',');
}

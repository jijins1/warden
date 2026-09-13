/**
 * Les contrats de fil de l'API, en miroir des structures Go.
 *
 * Écrits à la main faute d'OpenAPI côté serveur. Le risque assumé est la
 * dérive : chaque type porte donc le fichier Go dont il est le miroir, pour que
 * la vérification soit possible plutôt que théorique.
 */

/** `core.RunState` — internal/server/internal/core/run.go:13. Quatre, pas plus. */
export type RunState = 'running' | 'succeeded' | 'failed' | 'interrupted';

/** `core.TerminationCause` — run.go:27. Renseignée SI ET SEULEMENT SI `interrupted`. */
export type TerminationCause =
  | 'user_interrupt'
  | 'client_disconnect_timeout'
  | 'server_task_timeout'
  | 'pod_oom_killed'
  | 'pod_evicted'
  | 'node_lost'
  | 'admin_killed'
  | 'unknown';

/** `events.State` — internal/events/event.go:19. Jeu FERMÉ, validé côté serveur. */
export type EventState = 'ok' | 'changed' | 'failed' | 'skipped' | 'unreachable' | 'unknown';

/** `core.RunKind` — internal/server/internal/core/hosts.go:157. */
export type RunKind = 'playbook' | 'enrollment';

/**
 * `GET /runs/{run_ref}` — api/run_detail_handler.go.
 *
 * Ni `id` ni `user_id` : l'ADR-0021 a retiré l'énumération en exposant des ULID.
 */
export interface RunDetail {
  run_ref: string;
  repo: string;
  environment: string;
  inventory?: string;
  playbook?: string;
  kind: RunKind;

  audit_sha?: string;
  base_sha?: string;
  mainline_ref?: string;
  mainline_sha?: string;

  state: RunState;
  /** Absente sauf sur `interrupted`. Ne JAMAIS afficher l'état sans elle. */
  termination_cause?: TerminationCause;

  changed_count: number;
  /** La rédaction de l'ADR-0017 a échoué : des secrets peuvent être en clair. */
  secrets_unmasked: boolean;

  started_at: string;
  ended_at?: string;

  /** Le code du run a été purgé (ADR-0013). Le run a existé ; son arbre non. */
  artifact_pruned: boolean;
  artifact_pruned_at?: string;
}

/**
 * `GET /runs` — sérialise `core.RunListItem` TEL QUEL, donc en PascalCase.
 *
 * Incohérence antérieure et connue : le reste des contrats du produit est en
 * snake_case. Elle n'est pas corrigée ici parce que la CLI lit déjà cette forme
 * (voir ADR-0035, « Conséquences »). Le miroir la reproduit fidèlement plutôt
 * que de la maquiller, pour que la dérive reste visible.
 */
export interface RunListItem {
  RunRef: string;
  Repo: string;
  Environment: string;
  Inventory: string;
  Playbook: string;
  State: RunState;
  TerminationCause: TerminationCause | '';
  StartedAt: string;
  EndedAt: string | null;
  ChangedCount: number;
  SecretsUnmasked: boolean;
}

export interface RunListResult {
  Runs: RunListItem[];
  /** Vide sur la dernière page. Opaque : jamais un numéro de page. */
  NextCursor: string;
}

/**
 * `GET /hosts/{host}/runs` — un TABLEAU nu, pas une page.
 *
 * Ni curseur ni total : le serveur plafonne à 500 lignes et laisse resserrer par
 * `since`/`until`. La vue traverse les runs, donc elle est filtrée en Go et non
 * en SQL — mais elle reste restreinte à l'appelant, comme la liste.
 */
export type RunsParMachine = RunListItem[];

/** `POST /auth/password` — api/auth_handler.go. */
export interface ChangementMotDePasse {
  current_password: string;
  new_password: string;
}

/**
 * `GET /admin/repos` et `PUT /admin/repos/{repo}` — api/repo_handler.go.
 *
 * Réservé aux administrateurs côté serveur (404 sinon, pas 403 : voir le
 * commentaire de `HandleListRepos`) — la garde `adminGuard` évite juste un
 * écran qui répondrait 404 de toute façon.
 */
export interface Repo {
  name: string;
  remote_url: string;
  mainline_ref: string;
}

/** `GET /auth/whoami` — api/auth_handler.go. */
export interface Whoami {
  subject: string;
  name?: string;
  admin?: boolean;
  /** Vide en mode de développement : il n'y a pas eu de connexion. */
  expires_at?: string;
}

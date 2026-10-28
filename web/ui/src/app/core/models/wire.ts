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

  /** Le compte qui a lancé le run. `subject` fait foi ; le nom peut manquer. */
  author: { subject: string; display_name?: string };

  /**
   * Les drapeaux du run, dans l'ordre de `FlagsAnsible.Composer` : options
   * d'abord, `--extra-vars=nom=valeur` en dernier. Absent sans drapeau.
   *
   * Les valeurs d'`extra_vars` y sont EN CLAIR, et c'est voulu (ADR-0039) :
   * elles sont auditées. Les secrets passent par `forward_env`, dont les
   * valeurs ne sont jamais enregistrées — elles n'ont donc rien à faire ici.
   */
  flags?: string[];

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
  /** Le compte qui a lancé le run — `core.Auteur`, sérialisé tel quel. */
  Author: { Subject: string; DisplayName: string };
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

/**
 * `GET /hosts/{host}/playbooks` — core.PlaybookSummary TEL QUEL, même
 * PascalCase que RunListItem et pour la même raison (voir ci-dessus).
 *
 * Une ligne par (Repo, Playbook) : le run le plus récent de ce couple, pas son
 * historique. LastAuditSHA existe pour diffuser vs=current sans devoir relire
 * le run par sa référence au préalable.
 */
export interface PlaybookSummary {
  Repo: string;
  Playbook: string;
  Environment: string;
  Inventory: string;
  LastRunRef: string;
  LastUserID: number;
  LastAuditSHA: string;
  LastState: RunState;
  LastStartedAt: string;
}

/** `GET /hosts/{host}/playbooks` — un TABLEAU nu, comme RunsParMachine. */
export type PlaybooksParMachine = PlaybookSummary[];

/**
 * `core.EnrollmentMode` — internal/server/internal/core/hosts.go:55.
 *
 * `assisted` : warden a posé la clé lui-même, et a donc vu passer un mot de
 * passe. `declared` : l'opérateur l'a posée, warden n'a qu'enregistré.
 */
export type EnrollmentMode = 'declared' | 'assisted';

/** Une machine enrôlée — `core.Host` (hosts.go), en snake_case. */
export interface MachineEnrolee {
  host: string;
  environment: string;
  address: string;
  /** Absent vaut 22. */
  port?: number;
  login: string;
  /** L'empreinte épinglée ; vide tant que la machine n'a jamais été contactée. */
  host_key: string;
  mode: EnrollmentMode;
  enrolled_at: string;
}

/**
 * `GET /hosts?env=&q=&cursor=&limit=` — les machines des environnements où
 * l'appelant a un droit vivant ; un administrateur n'en voit pas plus. Triées
 * par environnement puis par nom. 400 sur un curseur illisible.
 */
export interface PageMachines {
  hosts: MachineEnrolee[];
  /** Vide sur la dernière page. Opaque : jamais un numéro de page. */
  next_cursor: string;
}

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

/** `core.KeyOrigin` — internal/server/internal/core/keys.go:101. */
export type KeyOrigin = 'generated' | 'imported';

/**
 * `GET /admin/keys` et `GET /admin/keys/{environment}` — api/key_handler.go.
 *
 * La partie PRIVÉE de la clé n'y figure jamais — elle descend vers le pod par
 * le canal de dispatch, un chemin distinct (ADR-0018). Réservé aux
 * administrateurs côté serveur, comme les dépôts.
 */
export interface KeyDescriptor {
  environment: string;
  public_key: string;
  fingerprint: string;
  origin: KeyOrigin;
  created_at: string;
  rotated_at?: string;
}

/**
 * `GET /environments/protected` — api/protected_handler.go.
 *
 * Un avertissement visuel, PAS un contrôle d'accès (ADR-0044) — voir
 * `EnvironmentGrant` pour ça. Ouvert à tout compte authentifié, pas
 * seulement aux administrateurs : le nom d'un environnement n'est pas
 * sensible, et le badge doit se colorer partout où il apparaît.
 */
export interface ProtectedEnvironments {
  environments: string[];
}

/**
 * `GET /environments` — api/environments_handler.go.
 *
 * Les environnements sur lesquels l'appelant a un droit VIVANT : la portée de
 * ses runs (`VisibleRunFilter`) et de ses clés de vault. Être administrateur
 * n'y ajoute rien.
 */
export interface MesEnvironnements {
  environments: string[];
}

/**
 * `GET|POST /environments/{env}/vault` — api/vault_handler.go, `vaultKeyPayload`.
 *
 * Aucun champ ne porte la clé en clair, et aucun n'en portera (ADR-0045).
 */
export interface VaultKeyDescriptor {
  environment: string;
  fingerprint: string;
  origin: KeyOrigin;
  created_at: string;
  rotated_at?: string;
}

/** `POST /environments/{env}/vault` — `putVaultKeyPayload`. Les deux modes sont exclusifs. */
export type PoseCleDeVault = { generate: true } | { plaintext: string };

/** `POST /environments/{env}/vault/encrypt` — `encryptPayload`. */
export interface ChiffrementVault {
  var_name: string;
  plaintext: string;
}

/** Réponse de `POST /environments/{env}/vault/encrypt` — `encryptedPayload`. */
export interface BlocVault {
  /** `nom: !vault |` suivi du texte chiffré, prêt à coller dans un YAML. */
  block: string;
}

/**
 * `POST /repos/{repo}/runs` — api/run_launch_handler.go, `launchRunPayload`.
 *
 * Ni `run_ref` ni `base_sha` : le serveur tire l'un et résout l'autre. Ni
 * `kind` ni `module` : un run lancé depuis une page est toujours un playbook
 * (ADR-0042), et le serveur REFUSE les champs inconnus — en envoyer un ici
 * donne un 400.
 *
 * `environment` est obligatoire ici alors qu'il est facultatif sur
 * `POST /runs` : voir le commentaire de `launchRunPayload` côté serveur — il
 * faut la principale pour lire la configuration, donc le `default_env` du
 * commit obligerait à travailler AVANT toute autorisation.
 */
export interface LancementRun {
  environment: string;
  inventory?: string;
  playbook: string;
  /** Déjà validés côté serveur par `core.FlagsDemandes` (ADR-0038, ADR-0039). */
  flags?: string[];
  /** Ne survit pas à la requête : consigne éphémère, jamais en base (ADR-0018). */
  become_password?: string;
  /** Les VALEURS seules ; les NOMS viennent du commit (ADR-0009). */
  forward_env?: Record<string, string>;
}

/** `createRunResponse` — api/run_handler.go. Partagée par `POST /runs` et `POST /repos/{repo}/runs`. */
export interface RunCree {
  run_ref: string;
  audit_sha: string;
  mainline_ref: string;
  mainline_sha: string;
  /** Vide quand le run est enregistré sans être démarré. */
  pod?: string;
}

/** `GET /auth/whoami` — api/auth_handler.go. */
export interface Whoami {
  subject: string;
  name?: string;
  admin?: boolean;
  /** Vide en mode de développement : il n'y a pas eu de connexion. */
  expires_at?: string;
}

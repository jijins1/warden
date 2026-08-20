// Run states
export type RunState = 'pending' | 'running' | 'succeeded' | 'failed' | 'cancelled' | 'unknown';

// Termination causes
export type TerminationCause = 'completed' | 'error' | 'cancelled' | 'timeout' | 'oom' | 'evicted' | 'unknown';

// Run kinds
export type RunKind = 'playbook' | 'enrollment' | '';

// User types
export type UserType = 'local' | 'oidc' | 'ldap';

// Identity from the backend
export interface Identity {
  type: UserType;
  subject: string;
  displayName: string;
  email: string;
}

// User from the backend
export interface User {
  id: number;
  type: UserType;
  subject: string;
  displayName: string;
  email: string;
  disabled: boolean;
  isAdmin: boolean;
}

// EnrollTarget for enrollment runs
export interface EnrollTarget {
  host: string;
  address: string;
  login: string;
  hostKey?: string;
}

// Main run model
export interface Run {
  id: number;
  runRef: string;
  userId: number;
  runDay: string; // ISO date
  repo: string;
  environment: string;
  inventory: string;
  playbook: string;
  kind: RunKind;
  enrollTarget?: EnrollTarget;
  auditSHA: string;
  baseSHA: string;
  mainlineRef: string;
  mainlineSHA: string;
  state: RunState;
  terminationCause: TerminationCause;
  changedCount: number;
  secretsUnmasked: boolean;
  artifactPrunedAt?: string; // ISO date
}

// List item (subset of Run for table display)
export interface RunListItem {
  id: number;
  runRef: string;
  userId: number;
  repo: string;
  environment: string;
  inventory: string;
  playbook: string;
  state: RunState;
  startedAt: string; // ISO date
  endedAt?: string; // ISO date
  changedCount: number;
  secretsUnmasked: boolean;
}

// Paginated result
export interface RunListResult {
  runs: RunListItem[];
  nextCursor: string;
}

// Filter for listing runs
export interface RunFilter {
  cursor?: string;
  limit?: number;
  environment?: string;
  inventory?: string;
  userId?: number;
  state?: RunState;
  since?: string; // ISO date
  until?: string; // ISO date
  changedMin?: number;
}

// Event types
export type EventType = 'playbook_start' | 'task_start' | 'task_end' | 'host_unreachable' | 
  'host_failed' | 'host_ok' | 'host_skipped' | 'host_changed' | 'run_start' | 'run_end' | 'error';

// Event model
export interface RunEvent {
  id: number;
  runId: number;
  seq: number;
  type: EventType;
  timestamp: string; // ISO date
  host?: string;
  task?: string;
  play?: string;
  message?: string;
  result?: string;
  changed?: boolean;
  duration?: number; // milliseconds
}

// Diff types
export interface GitDiff {
  runRef: string;
  path: string;
  oldPath?: string;
  diff: string; // Unified diff format
  newFile: boolean;
  deletedFile: boolean;
  binary: boolean;
}

// Summary stats
export interface RunSummary {
  totalRuns: number;
  succeeded: number;
  failed: number;
  running: number;
  pending: number;
  totalChanged: number;
  byEnvironment: Record<string, number>;
  byInventory: Record<string, number>;
  byUser: Record<number, number>;
}

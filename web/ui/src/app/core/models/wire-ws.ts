import { EventState } from './wire';

/** Cadre générique — internal/wire/ws/protocol.go. */
export interface Message {
  type: 'event' | 'end' | 'error' | 'keepalive';
  body: unknown;
}

/**
 * Un event du run.
 *
 * Il ne porte PAS de diff, et c'est délibéré côté serveur : les payloads
 * `--diff` vivent dans une table à contrôle d'accès distinct parce qu'ils
 * contiennent des secrets rendus. Les consulter est une action séparément
 * autorisée, pas un champ de plus.
 */
export interface EventFrame {
  seq: number;
  occurred_at: string;
  host?: string;
  task_number?: number;
  task_name?: string;
  role?: string;
  state: EventState;
  payload: unknown;
}

/** Le run est clos. `status` est relu en base, jamais supposé. */
export interface EndFrame {
  status: string;
  reason?: string;
}

export interface ErrorFrame {
  error: string;
}

import { execFileSync } from 'node:child_process';
import { resolve } from 'node:path';

/**
 * La stack `docker compose` que la suite pilote.
 *
 * Un projet À PART (`warden-e2e`) et non celui de la stack de dev (`warden`) :
 * le teardown fait `down -v`, et sous le même nom il effacerait la base d'un
 * développeur qui l'a laissée tourner. Les ports, eux, restent ceux de la stack
 * (8080, 4280) : si elle tourne déjà, `up` échoue sur un port pris — un échec
 * bruyant, plutôt que des tests passés contre la mauvaise instance.
 */
export const RACINE = resolve(__dirname, '../../..');
export const COMPOSE_FILE = resolve(RACINE, 'deploy/compose/compose.yaml');
export const PROJET = process.env['WARDEN_E2E_PROJECT'] ?? 'warden-e2e';

/** L'UI. `localhost` et non 127.0.0.1 : c'est l'origine que l'API autorise. */
export const UI_URL = 'http://localhost:4280';
/** Vues de Node, qui n'a pas le repli IPv6 → IPv4 d'un navigateur. */
export const API_SONDE = 'http://127.0.0.1:8080/version';
export const UI_SONDE = 'http://127.0.0.1:4280/config.json';

export function compose(args: string[], options: { silencieux?: boolean } = {}): string {
  return execFileSync('docker', ['compose', '-p', PROJET, '-f', COMPOSE_FILE, ...args], {
    cwd: RACINE,
    encoding: 'utf8',
    env: process.env,
    // Le build et le démarrage s'affichent : dix minutes muettes ressemblent à
    // un blocage.
    stdio: options.silencieux ? ['ignore', 'pipe', 'pipe'] : ['ignore', 'pipe', 'inherit'],
    maxBuffer: 64 * 1024 * 1024,
  });
}

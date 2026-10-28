import { rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { compose } from './stack';

/**
 * Détruit la stack, volumes compris : une exécution suivante repart d'une base
 * vide, sans comptes ni runs laissés par celle-ci.
 *
 * `WARDEN_E2E_KEEP=1` la garde, pour ouvrir http://localhost:4280 et regarder
 * ce qui a échoué. On l'arrête ensuite à la main :
 *   docker compose -p warden-e2e -f deploy/compose/compose.yaml down -v
 */
export default async function globalTeardown(): Promise<void> {
  if (process.env['WARDEN_E2E_KEEP'] === '1') {
    console.log('WARDEN_E2E_KEEP=1 : la stack warden-e2e reste en place.');
    return;
  }
  compose(['down', '-v', '--remove-orphans']);
  const forge = process.env['WARDEN_FORGE_DIR'];
  // Seulement le répertoire jetable que le setup a créé, jamais celui d'un
  // développeur passé par l'environnement.
  if (forge?.startsWith(tmpdir()) && forge.includes('warden-e2e-forge-')) {
    rmSync(forge, { recursive: true, force: true });
  }
}

import { defineConfig, devices } from '@playwright/test';
import { UI_URL } from './stack';

/**
 * La suite e2e de l'interface : un vrai navigateur contre la stack
 * `deploy/compose`, image de l'UI comprise. Elle prouve ce que les tests
 * Vitest ne peuvent pas : le CORS, la CSP, le cookie de session et
 * `config.json` tels que le navigateur les applique.
 *
 *   npm run e2e                 (depuis web/ui ; la stack est démarrée ici)
 *   make test-e2e-ui            (depuis la racine ; construit d'abord les images)
 */
export default defineConfig({
  testDir: '.',
  testMatch: '*.spec.ts',
  globalSetup: './global-setup.ts',
  globalTeardown: './global-teardown.ts',
  // Un seul worker : la suite est courte, et des connexions simultanées sur le
  // même compte n'ajouteraient que du bruit au diagnostic.
  workers: 1,
  fullyParallel: false,
  forbidOnly: !!process.env['CI'],
  retries: 0,
  timeout: 30_000,
  expect: { timeout: 10_000 },
  reporter: [['list'], ['html', { outputFolder: 'playwright-report', open: 'never' }]],
  outputDir: 'test-results',
  use: {
    baseURL: UI_URL,
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  // Chromium seul : la suite vérifie l'intégration avec la stack, pas la
  // compatibilité entre navigateurs.
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
});

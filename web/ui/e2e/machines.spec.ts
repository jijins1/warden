import { expect, test } from '@playwright/test';
import { seConnecterEnAdmin } from './identifiants';

test.describe('machines', () => {
  test.beforeEach(async ({ page }) => {
    await seConnecterEnAdmin(page);
  });

  // Une stack neuve n'a aucune machine enrôlée. Et le compte de la suite, créé
  // à chaque exécution, n'a aucun droit d'environnement (deploy/compose/grants.yaml
  // ne le nomme pas) : la liste est vide même pour un administrateur, puisque
  // l'être n'élargit pas la portée.
  test('/hosts montre la liste, vide sur une stack neuve', async ({ page }) => {
    await page.goto('/hosts');

    await expect(page.getByRole('heading', { level: 1, name: 'Machines' })).toBeVisible();
    await expect(page.getByText('No machine matches.')).toBeVisible();
  });
});

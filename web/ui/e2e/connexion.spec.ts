import { expect, test } from '@playwright/test';
import { compte, seConnecter, seConnecterEnAdmin } from './identifiants';

test.describe('connexion', () => {
  test('un mauvais mot de passe est refusé et laisse sur /login', async ({ page }) => {
    await seConnecter(page, compte().subject, 'pas-le-bon-mot-de-passe');

    await expect(page.getByRole('alert')).toBeVisible();
    await expect(page).toHaveURL(/\/login$/);
  });

  test('le bon mot de passe ouvre la liste des runs, vide', async ({ page }) => {
    await seConnecterEnAdmin(page);

    await expect(page.getByRole('heading', { level: 1, name: 'Runs' })).toBeVisible();
    // Une stack neuve : aucun run n'a été soumis.
    await expect(page.getByText('No run matches.')).toBeVisible();
  });

  test('« Sign out » ferme la session', async ({ page }) => {
    const { subject } = compte();
    await seConnecterEnAdmin(page);

    // Le menu utilisateur porte le sujet du compte connecté.
    await page.getByRole('button', { name: subject }).click();
    await page.getByRole('menuitem', { name: 'Sign out' }).click();
    await expect(page).toHaveURL(/\/login/);

    // La session est fermée côté serveur, pas seulement oubliée de l'écran :
    // un lien profond redemande la connexion.
    await page.goto('/runs');
    await expect(page).toHaveURL(/\/login/);
    await expect(page.getByRole('button', { name: 'Sign in' })).toBeVisible();
  });
});

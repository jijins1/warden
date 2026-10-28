import { expect, test } from '@playwright/test';
import { compte, seConnecterEnAdmin } from './identifiants';

/** Chaque entrée de la barre, et la page où elle mène. */
const PAGES = [
  { lien: 'Runs', url: /\/runs$/ },
  { lien: 'Machines', url: /\/hosts$/ },
  { lien: 'Vault', url: /\/vault$/ },
  // Réservées aux administrateurs ; le compte de la suite en est un.
  { lien: 'Repos', url: /\/repos$/ },
  { lien: 'Environments', url: /\/environments$/ },
];

test.describe('navigation', () => {
  test.beforeEach(async ({ page }) => {
    await seConnecterEnAdmin(page);
  });

  test('la barre montre toutes les entrées d\'un administrateur', async ({ page }) => {
    const barre = page.getByRole('navigation');
    for (const { lien } of PAGES) {
      await expect(barre.getByRole('link', { name: lien, exact: true })).toBeVisible();
    }
  });

  for (const { lien, url } of PAGES) {
    test(`« ${lien} » mène à sa page`, async ({ page }) => {
      await page.getByRole('navigation').getByRole('link', { name: lien, exact: true }).click();

      await expect(page).toHaveURL(url);
      // La page a rendu, et non une erreur ou la redirection d'une garde.
      await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
    });
  }

  // Le compte n'est PAS dans la barre : il vit dans le menu utilisateur, et
  // l'y mettre deux fois faisait doublon.
  test('« Account » passe par le menu utilisateur, pas par la barre', async ({ page }) => {
    const barre = page.getByRole('navigation');
    await expect(barre.getByRole('link', { name: 'Account', exact: true })).toHaveCount(0);

    await page.getByRole('button', { name: compte().subject }).click();
    await page.getByRole('menuitem', { name: 'Account' }).click();

    await expect(page).toHaveURL(/\/account$/);
    await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
  });
});

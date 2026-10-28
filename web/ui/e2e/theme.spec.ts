import { expect, test, type Page } from '@playwright/test';

/**
 * Le thème suit la préférence du système, et le sombre est le défaut du design
 * system. Vérifié sur /login : la page n'exige pas de session, et elle porte
 * déjà le fond de l'application.
 */
async function fondDuBody(page: Page): Promise<string> {
  return page.evaluate(() => getComputedStyle(document.body).backgroundColor);
}

test.describe('thème', () => {
  test.describe('préférence sombre', () => {
    test.use({ colorScheme: 'dark' });

    test('le fond est celui du thème sombre', async ({ page }) => {
      await page.goto('/login');
      await expect.poll(() => fondDuBody(page)).toBe('rgb(11, 13, 16)');
    });
  });

  test.describe('préférence claire', () => {
    test.use({ colorScheme: 'light' });

    test('le fond est celui du thème clair', async ({ page }) => {
      await page.goto('/login');
      await expect.poll(() => fondDuBody(page)).toBe('rgb(246, 247, 249)');
    });
  });
});

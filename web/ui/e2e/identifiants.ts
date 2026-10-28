import { expect, type Page } from '@playwright/test';

/** Le compte que global-setup.ts a créé pour cette exécution. */
export function compte(): { subject: string; motDePasse: string } {
  const subject = process.env['WARDEN_E2E_SUBJECT'];
  const motDePasse = process.env['WARDEN_E2E_PASSWORD'];
  if (!subject || !motDePasse) {
    throw new Error('compte e2e absent : global-setup.ts n\'a pas tourné');
  }
  return { subject, motDePasse };
}

/** Remplit l'écran de connexion. Ne vérifie PAS l'issue : c'est au test. */
export async function seConnecter(page: Page, subject: string, motDePasse: string): Promise<void> {
  await page.goto('/login');
  await page.getByLabel('Subject').fill(subject);
  await page.getByLabel('Password').fill(motDePasse);
  await page.getByRole('button', { name: 'Sign in' }).click();
}

/** Se connecte avec le compte de la suite et attend la liste des runs. */
export async function seConnecterEnAdmin(page: Page): Promise<void> {
  const { subject, motDePasse } = compte();
  await seConnecter(page, subject, motDePasse);
  await expect(page).toHaveURL(/\/runs$/);
}

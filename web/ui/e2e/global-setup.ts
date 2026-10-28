import { mkdtempSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { API_SONDE, UI_SONDE, compose } from './stack';

/**
 * Démarre la stack et crée le compte de la suite.
 *
 * Les identifiants passent par `process.env` : Playwright le transmet aux
 * workers, qui démarrent après ce setup. Le sujet est unique à chaque exécution
 * pour que la suite tourne aussi sur une stack gardée (`WARDEN_E2E_KEEP=1`),
 * où `user add` refuserait un sujet déjà pris.
 */
export default async function globalSetup(): Promise<void> {
  // La forge sert un répertoire de l'hôte ; sans valeur, compose créerait
  // `deploy/compose/forge` en root. La suite n'en a pas l'usage : un
  // répertoire jetable suffit.
  process.env['WARDEN_FORGE_DIR'] ??= mkdtempSync(join(tmpdir(), 'warden-e2e-forge-'));

  try {
    // `--wait` attend les healthchecks (postgres, vault, ui) et que les
    // services sans healthcheck tournent ; il échoue si l'un d'eux s'arrête.
    //
    // `--build` par défaut, pour qu'un `npm run e2e` teste l'UI du répertoire
    // de travail et non une image d'hier. `make test-e2e-ui` pose
    // WARDEN_E2E_BUILD=0 : il vient de construire les images, et compose
    // construit encore celles qui manquent.
    const construire = process.env['WARDEN_E2E_BUILD'] === '0' ? [] : ['--build'];
    compose(['up', '-d', ...construire, '--wait', '--wait-timeout', '300']);
    // L'API n'a pas de healthcheck : « en cours d'exécution » n'est pas « prête
    // à répondre ».
    await attendre(API_SONDE);
    await attendre(UI_SONDE);
  } catch (e) {
    // Sans les journaux, un échec de démarrage ne dit que « exit code 1 ».
    try {
      console.error(compose(['ps', '-a'], { silencieux: true }));
      console.error(compose(['logs', '--tail', '50'], { silencieux: true }));
    } catch {
      /* rien de plus à montrer */
    }
    throw e;
  }

  const subject = `e2e-${Date.now().toString(36)}`;
  const sortie = compose(['exec', '-T', 'api', 'wardend', 'user', 'add', '--subject', subject, '--admin'], {
    silencieux: true,
  });
  // Voir ajouterCompte (internal/server/bootstrap.go) : « mot de passe  <mdp> ».
  const motDePasse = /mot de passe\s+(\S+)/.exec(sortie)?.[1];
  if (!motDePasse) {
    throw new Error(`mot de passe introuvable dans la sortie de \`user add\` :\n${sortie}`);
  }
  process.env['WARDEN_E2E_SUBJECT'] = subject;
  process.env['WARDEN_E2E_PASSWORD'] = motDePasse;
}

async function attendre(url: string, delaiMs = 120_000): Promise<void> {
  const fin = Date.now() + delaiMs;
  let derniere: unknown;
  while (Date.now() < fin) {
    try {
      const r = await fetch(url);
      if (r.ok) return;
      derniere = `HTTP ${r.status}`;
    } catch (e) {
      derniere = e;
    }
    await new Promise((ok) => setTimeout(ok, 1_000));
  }
  throw new Error(`${url} ne répond pas après ${delaiMs / 1000} s : ${String(derniere)}`);
}

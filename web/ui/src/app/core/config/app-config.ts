import { InjectionToken, inject } from '@angular/core';

/**
 * Configuration résolue à l'EXÉCUTION, jamais à la compilation.
 *
 * Un artefact statique est déployé tel quel sur dev puis sur prod : compiler
 * l'adresse de l'API dans le bundle imposerait un build par environnement, donc
 * de reconstruire pour promouvoir — et de ne plus déployer exactement ce qui a
 * été testé.
 */
export interface AppConfig {
  /** Racine de l'API, sans barre finale. Origine DISTINCTE de celle de l'UI. */
  readonly apiUrl: string;
}

export const APP_CONFIG = new InjectionToken<AppConfig>('APP_CONFIG');

/** Adresse absolue d'un chemin d'API. */
export function apiUrl(chemin: string): string {
  const base = inject(APP_CONFIG).apiUrl.replace(/\/+$/, '');
  return `${base}${chemin.startsWith('/') ? chemin : `/${chemin}`}`;
}

/**
 * Charge `config.json` avant le premier rendu.
 *
 * L'échec n'est PAS rattrapé par un défaut : une UI qui se rabat silencieusement
 * sur une adresse devinée interroge le mauvais serveur, et sur un produit
 * d'audit cela veut dire montrer les runs d'un autre parc en croyant lire le
 * sien. Mieux vaut ne pas démarrer.
 */
export async function chargerConfig(): Promise<AppConfig> {
  const reponse = await fetch('config.json', { cache: 'no-store' });
  if (!reponse.ok) {
    throw new Error(`config.json illisible : HTTP ${reponse.status}`);
  }
  const brut = (await reponse.json()) as Partial<AppConfig>;
  if (!brut.apiUrl) {
    throw new Error('config.json ne porte pas `apiUrl`');
  }
  return { apiUrl: brut.apiUrl };
}

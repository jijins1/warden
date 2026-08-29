import { Injectable, effect, signal } from '@angular/core';

export type Theme = 'light' | 'dark' | 'system';

const CLE = 'warden.theme';

/**
 * Le thème, posé en attribut sur `<html>`.
 *
 * `system` est le défaut et n'écrit AUCUN attribut : la feuille de style laisse
 * alors `color-scheme: light dark` déférer à la préférence du système. Choisir
 * explicitement écrit `data-theme`, que les jetons surchargent.
 *
 * Le sombre est le défaut PRODUIT — un outil d'exploitation se lit longtemps —
 * mais il est offert et non imposé : forcer un thème contre la préférence
 * déclarée du poste est le genre de détail qui fatigue sans rien apporter.
 */
@Injectable({ providedIn: 'root' })
export class ThemeService {
  readonly #theme = signal<Theme>(lire());
  readonly theme = this.#theme.asReadonly();

  constructor() {
    effect(() => {
      const t = this.#theme();
      const racine = document.documentElement;
      if (t === 'system') {
        racine.removeAttribute('data-theme');
      } else {
        racine.setAttribute('data-theme', t);
      }
      try {
        localStorage.setItem(CLE, t);
      } catch {
        // Stockage refusé (navigation privée, politique d'entreprise) : le thème
        // vaut pour la session et rien ne casse. Il n'y a rien à signaler à
        // l'utilisateur, qui n'y peut rien.
      }
    });
  }

  poser(t: Theme): void {
    this.#theme.set(t);
  }
}

function lire(): Theme {
  try {
    const brut = localStorage.getItem(CLE);
    if (brut === 'light' || brut === 'dark' || brut === 'system') {
      return brut;
    }
  } catch {
    // idem : illisible vaut « pas de préférence ».
  }
  return 'system';
}

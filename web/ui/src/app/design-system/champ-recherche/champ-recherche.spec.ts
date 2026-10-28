import { TestBed } from '@angular/core/testing';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ChampRecherche } from './champ-recherche';

function monter() {
  const fixture = TestBed.createComponent(ChampRecherche);
  fixture.componentRef.setInput('placeholder', 'Search…');
  fixture.componentRef.setInput('libelle', 'Search things');
  fixture.detectChanges();
  // Attaché au document : un élément détaché ne peut pas prendre le focus.
  document.body.appendChild(fixture.nativeElement as HTMLElement);
  const emis: (string | null)[] = [];
  fixture.componentInstance.recherche.subscribe((v) => emis.push(v));
  const champ = (fixture.nativeElement as HTMLElement).querySelector('input') as HTMLInputElement;
  return { fixture, champ, emis };
}

function taper(champ: HTMLInputElement, texte: string): void {
  champ.value = texte;
  champ.dispatchEvent(new Event('input'));
}

describe('ChampRecherche', () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it('émet une fois la frappe posée, pas à chaque touche', () => {
    const { champ, emis } = monter();
    taper(champ, 'd');
    taper(champ, 'db');
    expect(emis).toEqual([]);

    vi.advanceTimersByTime(250);
    expect(emis).toEqual(['db']);
  });

  it('émet null pour un champ vidé', () => {
    const { champ, emis } = monter();
    taper(champ, '  ');
    vi.advanceTimersByTime(250);
    expect(emis).toEqual([null]);
  });

  it('« / » donne le focus au champ', () => {
    const { champ } = monter();
    document.body.dispatchEvent(new KeyboardEvent('keydown', { key: '/', bubbles: true }));
    expect(document.activeElement).toBe(champ);
  });

  // Dans un autre champ, `/` est un caractère qu'on tape : le détourner
  // empêcherait d'écrire un chemin.
  it('« / » tapé dans un autre champ reste un caractère', () => {
    const { champ } = monter();
    const autre = document.createElement('input');
    document.body.appendChild(autre);
    autre.focus();

    const evenement = new KeyboardEvent('keydown', { key: '/', bubbles: true, cancelable: true });
    autre.dispatchEvent(evenement);

    expect(evenement.defaultPrevented).toBe(false);
    expect(document.activeElement).not.toBe(champ);
    autre.remove();
  });
});

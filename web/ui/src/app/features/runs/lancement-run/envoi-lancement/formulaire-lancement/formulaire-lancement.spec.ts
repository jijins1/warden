import { Component, signal } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { describe, expect, it } from 'vitest';
import {
  FormulaireLancement,
  PreremplissageLancement,
  ValeursLancement,
} from './formulaire-lancement';

@Component({
  imports: [FormulaireLancement],
  template: `<w-formulaire-lancement
    [valeurInitiale]="valeurInitiale()"
    [enCours]="enCours()"
    (lancement)="emis.push($event)" />`,
})
class Hote {
  readonly valeurInitiale = signal<PreremplissageLancement | null>(null);
  readonly enCours = signal(false);
  readonly emis: ValeursLancement[] = [];
}

async function monter(valeur: PreremplissageLancement | null) {
  const fixture = TestBed.createComponent(Hote);
  fixture.componentInstance.valeurInitiale.set(valeur);
  await fixture.whenStable();
  const el = fixture.nativeElement as HTMLElement;
  return { fixture, el, hote: fixture.componentInstance };
}

const complet: PreremplissageLancement = {
  repo: 'depot',
  environment: 'prod',
  inventory: '',
  playbook: 'site.yml',
};

function envoi(el: HTMLElement): HTMLButtonElement {
  return el.querySelector('button[type="submit"]') as HTMLButtonElement;
}

describe('FormulaireLancement', () => {
  it('n’émet rien tant que dépôt, environnement et playbook manquent', async () => {
    const { el, hote, fixture } = await monter({ ...complet, playbook: '' });
    expect(envoi(el).disabled).toBe(true);

    el.querySelector('form')!.dispatchEvent(new Event('submit'));
    await fixture.whenStable();
    expect(hote.emis).toEqual([]);
  });

  it('émet la valeur complète une fois valide', async () => {
    const { el, hote, fixture } = await monter(complet);
    expect(envoi(el).disabled).toBe(false);

    el.querySelector('form')!.dispatchEvent(new Event('submit'));
    await fixture.whenStable();
    expect(hote.emis).toEqual([
      {
        ...complet,
        check: false,
        diff: false,
        become: false,
        becomeUser: '',
        becomePassword: '',
        limit: '',
        verbosity: 0,
        extraVars: [],
      },
    ]);
  });

  it('pendant l’envoi, le bouton est désactivé et le dit', async () => {
    const { el, hote, fixture } = await monter(complet);
    hote.enCours.set(true);
    await fixture.whenStable();
    expect(envoi(el).disabled).toBe(true);
    expect(envoi(el).textContent).toContain('Launching…');
  });

  it('--become révèle Become user et Become password', async () => {
    const { el, fixture } = await monter(complet);
    expect(el.querySelector('#lancement-become-user')).toBeNull();

    const become = Array.from(el.querySelectorAll<HTMLInputElement>('input[type="checkbox"]'))[2];
    become.click();
    await fixture.whenStable();
    expect(el.querySelector('#lancement-become-user')).not.toBeNull();
    expect(el.querySelector('#lancement-become-password')).not.toBeNull();
  });

  it('ajoute et retire des lignes de variables', async () => {
    const { el, fixture } = await monter(complet);
    const ajouter = Array.from(el.querySelectorAll('button')).find((b) =>
      b.textContent?.includes('Add variable'),
    )!;
    ajouter.click();
    ajouter.click();
    await fixture.whenStable();
    expect(el.querySelectorAll('[aria-label="Remove variable"]').length).toBe(2);

    (el.querySelector('[aria-label="Remove variable"]') as HTMLButtonElement).click();
    await fixture.whenStable();
    expect(el.querySelectorAll('[aria-label="Remove variable"]').length).toBe(1);
  });
});

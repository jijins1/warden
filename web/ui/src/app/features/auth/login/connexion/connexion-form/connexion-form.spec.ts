import { TestBed } from '@angular/core/testing';
import { describe, expect, it } from 'vitest';
import { ConnexionForm, Identifiants } from './connexion-form';

function monter() {
  const fixture = TestBed.createComponent(ConnexionForm);
  const emis: Identifiants[] = [];
  fixture.componentInstance.soumis.subscribe((v) => emis.push(v));
  fixture.detectChanges();
  const el = fixture.nativeElement as HTMLElement;
  const subject = el.querySelector<HTMLInputElement>('#connexion-subject')!;
  const password = el.querySelector<HTMLInputElement>('#connexion-password')!;
  const bouton = el.querySelector<HTMLButtonElement>('button[type="submit"]')!;
  return { fixture, el, emis, subject, password, bouton };
}

function saisir(input: HTMLInputElement, valeur: string) {
  input.value = valeur;
  input.dispatchEvent(new Event('input'));
}

describe('ConnexionForm', () => {
  it('relie chaque libellé à son champ', () => {
    const { el } = monter();
    const libelles = Array.from(el.querySelectorAll('label')).map((l) => [l.textContent?.trim(), l.htmlFor]);
    expect(libelles).toEqual([
      ['Subject', 'connexion-subject'],
      ['Password', 'connexion-password'],
    ]);
  });

  it('n’envoie rien tant qu’un des deux champs est vide', () => {
    const { fixture, emis, subject, bouton } = monter();
    saisir(subject, 'ruokki');
    fixture.detectChanges();

    expect(bouton.disabled).toBe(true);
    fixture.nativeElement.querySelector('form').dispatchEvent(new Event('submit'));
    expect(emis).toEqual([]);
  });

  // Retaper son identifiant après une faute de frappe sur le mot de passe
  // n'apporte rien : seul le mot de passe est vidé.
  it('émet les identifiants, vide le mot de passe et garde le sujet', () => {
    const { fixture, el, emis, subject, password } = monter();
    saisir(subject, 'ruokki');
    saisir(password, 'secret');
    fixture.detectChanges();
    el.querySelector('form')!.dispatchEvent(new Event('submit'));
    fixture.detectChanges();

    expect(emis).toEqual([{ subject: 'ruokki', password: 'secret' }]);
    expect(subject.value).toBe('ruokki');
    expect(password.value).toBe('');
  });

  it('annonce l’envoi en cours', () => {
    const { fixture, bouton } = monter();
    fixture.componentRef.setInput('enCours', true);
    fixture.detectChanges();

    expect(bouton.textContent?.trim()).toBe('Signing in…');
    expect(bouton.disabled).toBe(true);
  });
});

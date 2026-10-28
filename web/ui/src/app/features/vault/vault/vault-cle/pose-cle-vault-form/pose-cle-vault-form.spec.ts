import { TestBed } from '@angular/core/testing';
import { describe, expect, it } from 'vitest';
import { PoseCleVaultForm } from './pose-cle-vault-form';
import { PoseCleDeVault } from '../../../../../core/models/wire';

function monter(rotation: boolean) {
  const fixture = TestBed.createComponent(PoseCleVaultForm);
  fixture.componentRef.setInput('rotation', rotation);
  const emis: PoseCleDeVault[] = [];
  fixture.componentInstance.posee.subscribe((p) => emis.push(p));
  const el = fixture.nativeElement as HTMLElement;
  const soumettre = async () => {
    el.querySelector('form')!.dispatchEvent(new Event('submit'));
    await fixture.whenStable();
  };
  return { fixture, el, emis, soumettre };
}

describe('PoseCleVaultForm', () => {
  it('émet une demande de génération par défaut', async () => {
    const { fixture, emis, soumettre } = monter(false);
    await fixture.whenStable();
    await soumettre();

    expect(emis).toEqual([{ generate: true }]);
  });

  // La rotation laisse le contenu déjà chiffré du dépôt sur l'ancienne clé
  // (ADR-0045) : elle ne part pas sans qu'on l'ait reconnu.
  it('ne fait pas tourner la clé sans confirmation', async () => {
    const { fixture, el, emis, soumettre } = monter(true);
    await fixture.whenStable();
    await soumettre();
    expect(emis).toEqual([]);

    el.querySelector<HTMLInputElement>('input[type="checkbox"]')!.click();
    await fixture.whenStable();
    await soumettre();
    expect(emis).toEqual([{ generate: true }]);
  });

  it('exige le mot de passe en mode import, et l’envoie seul', async () => {
    const { fixture, el, emis, soumettre } = monter(false);
    await fixture.whenStable();
    el.querySelectorAll<HTMLInputElement>('input[type="radio"]')[1].click();
    await fixture.whenStable();
    await soumettre();
    expect(emis).toEqual([]);

    const champ = el.querySelector<HTMLInputElement>('input[type="password"]')!;
    champ.value = 's3cret';
    champ.dispatchEvent(new Event('input'));
    await fixture.whenStable();
    await soumettre();
    expect(emis).toEqual([{ plaintext: 's3cret' }]);
  });
});

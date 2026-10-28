import { TestBed } from '@angular/core/testing';
import { describe, expect, it, vi } from 'vitest';
import { VaultChiffrement } from './vault-chiffrement';
import { VaultService } from '../../../../core/services/vault/vault.service';

function monter(chiffrer: () => Promise<unknown>) {
  const service = { decrire: vi.fn(), poser: vi.fn(), chiffrer: vi.fn(chiffrer) };
  TestBed.configureTestingModule({ providers: [{ provide: VaultService, useValue: service }] });
  const fixture = TestBed.createComponent(VaultChiffrement);
  fixture.componentRef.setInput('environnement', 'prod');
  const el = fixture.nativeElement as HTMLElement;
  const saisir = (selecteur: string, valeur: string) => {
    const champ = el.querySelector<HTMLInputElement | HTMLTextAreaElement>(selecteur)!;
    champ.value = valeur;
    champ.dispatchEvent(new Event('input'));
  };
  const chiffrerValeur = async () => {
    saisir('input', 'my_secret');
    saisir('textarea', 's3cret');
    await fixture.whenStable();
    el.querySelector('form')!.dispatchEvent(new Event('submit'));
    await fixture.whenStable();
  };
  return { service, fixture, el, chiffrerValeur };
}

describe('VaultChiffrement', () => {
  it('chiffre avec la clé de l’environnement et affiche le bloc', async () => {
    const { service, fixture, el, chiffrerValeur } = monter(() =>
      Promise.resolve({ block: 'my_secret: !vault |\n  $ANSIBLE_VAULT;1.2;AES256;prod' }),
    );
    await fixture.whenStable();
    await chiffrerValeur();

    expect(service.chiffrer).toHaveBeenCalledWith('prod', {
      var_name: 'my_secret',
      plaintext: 's3cret',
    });
    expect(el.querySelector('pre')?.textContent).toContain('$ANSIBLE_VAULT;1.2;AES256;prod');
  });

  // Un bloc chiffré pour un environnement ne se déchiffrerait pas sous un autre.
  it('efface le bloc quand l’environnement change', async () => {
    const { fixture, el, chiffrerValeur } = monter(() =>
      Promise.resolve({ block: 'my_secret: !vault |' }),
    );
    await fixture.whenStable();
    await chiffrerValeur();
    expect(el.querySelector('pre')).not.toBeNull();

    fixture.componentRef.setInput('environnement', 'staging');
    await fixture.whenStable();

    expect(el.querySelector('pre')).toBeNull();
  });
});

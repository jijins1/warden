import { Component } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { describe, expect, it } from 'vitest';
import { EnvBadge } from './env-badge';

@Component({ imports: [EnvBadge], template: '<w-env-badge [env]="env" [protege]="protege" />' })
class Hote {
  env = 'prod';
  protege = false;
}

async function rendre(env: string, protege: boolean) {
  const fixture = TestBed.createComponent(Hote);
  fixture.componentInstance.env = env;
  fixture.componentInstance.protege = protege;
  await fixture.whenStable();
  return (fixture.nativeElement as HTMLElement).querySelector('.w-env')!;
}

describe('EnvBadge', () => {
  // Le statut vient de l'appelant, pas du nom : `prod` non déclaré n'est pas
  // distingué, et `preprod` déclaré l'est.
  it('suit le statut reçu, pas le nom', async () => {
    expect((await rendre('prod', false)).classList.contains('is-protected')).toBe(false);
    expect((await rendre('preprod', true)).classList.contains('is-protected')).toBe(true);
  });

  it('affiche le nom tel quel', async () => {
    expect((await rendre('staging', false)).textContent?.trim()).toBe('staging');
  });
});

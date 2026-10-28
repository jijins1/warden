import { Component } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { describe, expect, it, vi } from 'vitest';
import { BadgeEnvironnement } from './badge-environnement';
import { ProtectedEnvironmentsService } from '../../core/services/protected-environments/protected-environments.service';

@Component({ imports: [BadgeEnvironnement], template: '<w-badge-environnement [env]="env" />' })
class Hote {
  env = 'prod';
}

function monter(env: string, proteges: string[]) {
  const service = {
    charger: vi.fn(),
    isProtected: (e: string) => proteges.includes(e.toLowerCase()),
  };
  TestBed.configureTestingModule({
    providers: [{ provide: ProtectedEnvironmentsService, useValue: service }],
  });
  const fixture = TestBed.createComponent(Hote);
  fixture.componentInstance.env = env;
  return { fixture, service };
}

describe('BadgeEnvironnement', () => {
  // LE point de l'ADR-0044 : ce n'est plus une comparaison de nom, mais une
  // question posée au service. `preprod` n'est pas `prod`, et doit pourtant
  // pouvoir être marqué protégé.
  it('marque protégé un environnement déclaré, même si son nom diffère de prod', async () => {
    const { fixture } = monter('preprod', ['preprod']);
    await fixture.whenStable();
    expect(fixture.nativeElement.querySelector('.w-env').classList.contains('is-protected')).toBe(
      true,
    );
  });

  it("ne marque pas prod si le service ne le déclare pas protégé", async () => {
    const { fixture } = monter('prod', []);
    await fixture.whenStable();
    expect(fixture.nativeElement.querySelector('.w-env').classList.contains('is-protected')).toBe(
      false,
    );
  });

  it('déclenche le chargement au montage', () => {
    const { service } = monter('prod', ['prod']);
    expect(service.charger).toHaveBeenCalled();
  });
});

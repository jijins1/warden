import { TestBed } from '@angular/core/testing';
import { describe, expect, it, vi } from 'vitest';
import { VaultCle } from './vault-cle';
import { VaultService } from '../../core/api/vault.service';

function monter(decrire: () => Promise<unknown>) {
  const service = { decrire: vi.fn(decrire), poser: vi.fn(), chiffrer: vi.fn() };
  TestBed.configureTestingModule({ providers: [{ provide: VaultService, useValue: service }] });
  const fixture = TestBed.createComponent(VaultCle);
  fixture.componentRef.setInput('environnement', 'prod');
  return { service, fixture, el: fixture.nativeElement as HTMLElement };
}

describe('VaultCle', () => {
  // Un 404 n'est pas une panne : l'environnement n'a pas encore de clé, et
  // l'écran doit proposer d'en poser une, pas afficher un bandeau rouge.
  it('lit un 404 comme « pas encore de clé »', async () => {
    const { service, fixture, el } = monter(() =>
      Promise.reject({ status: 404, cause: 'not-found', message: 'not found' }),
    );
    await fixture.whenStable();

    expect(service.decrire).toHaveBeenCalledWith('prod');
    expect(el.querySelector('[role="alert"]')).toBeNull();
    expect(el.textContent).toContain('no vault key yet');
    expect(el.textContent).toContain('Set the key');
  });

  it('affiche l’empreinte et propose la rotation quand la clé existe', async () => {
    const { fixture, el } = monter(() =>
      Promise.resolve({
        environment: 'prod',
        fingerprint: 'sha256:abc',
        origin: 'generated',
        created_at: '2026-10-01T10:00:00Z',
      }),
    );
    await fixture.whenStable();

    expect(el.textContent).toContain('sha256:abc');
    expect(el.textContent).toContain('Rotate the key');
  });

  it('signale une vraie panne de lecture', async () => {
    const { fixture, el } = monter(() =>
      Promise.reject({ status: 500, cause: 'server', message: 'could not read the vault key' }),
    );
    await fixture.whenStable();

    expect(el.querySelector('[role="alert"]')?.textContent).toContain('could not read the vault key');
  });
});

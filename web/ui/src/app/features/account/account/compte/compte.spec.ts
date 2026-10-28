import { signal } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { describe, expect, it } from 'vitest';
import { Compte } from './compte';
import { AuthService } from '../../../../core/services/auth/auth.service';
import { Whoami } from '../../../../core/models/wire';

function monter(moi: Whoami | null) {
  TestBed.configureTestingModule({
    providers: [{ provide: AuthService, useValue: { utilisateur: signal(moi) } }],
  });
  const fixture = TestBed.createComponent(Compte);
  fixture.detectChanges();
  return fixture.nativeElement as HTMLElement;
}

describe('Compte', () => {
  // Sans échéance, il n'y a pas eu de connexion : l'identité est affirmée par
  // un en-tête de développement, et l'écran doit le dire.
  it('signale une identité affirmée quand la session n’a pas d’échéance', () => {
    const el = monter({ subject: 'ruokki' });
    expect(el.textContent).toContain('asserted, not verified');
  });

  it('ne signale rien pour une vraie session', () => {
    const el = monter({ subject: 'ruokki', expires_at: '2026-10-28T12:00:00Z' });
    expect(el.textContent).toContain('ruokki');
    expect(el.textContent).not.toContain('asserted, not verified');
  });

  // Être administrateur n'ouvre pas les runs d'autrui : deux capacités séparées.
  it('rappelle à un administrateur ce que son drapeau n’accorde pas', () => {
    const el = monter({ subject: 'ruokki', admin: true, expires_at: '2026-10-28T12:00:00Z' });
    expect(el.textContent).toContain('does not widen what you');
  });

  it('n’affiche rien sans utilisateur', () => {
    const el = monter(null);
    expect(el.textContent?.trim()).toBe('');
  });
});

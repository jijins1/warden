import { TestBed } from '@angular/core/testing';
import { Router } from '@angular/router';
import { describe, expect, it, vi } from 'vitest';
import { Connexion } from './connexion';
import { AuthService } from '../../../../core/services/auth/auth.service';

function monter(connexion: () => Promise<unknown>) {
  const auth = { connexion: vi.fn(connexion) };
  const router = { navigateByUrl: vi.fn(() => Promise.resolve(true)) };
  TestBed.configureTestingModule({
    providers: [
      { provide: AuthService, useValue: auth },
      { provide: Router, useValue: router },
    ],
  });
  const fixture = TestBed.createComponent(Connexion);
  fixture.componentRef.setInput('retour', '/runs/abc');
  fixture.detectChanges();
  const el = fixture.nativeElement as HTMLElement;
  return { auth, router, fixture, el };
}

async function soumettre(el: HTMLElement, fixture: { whenStable(): Promise<unknown>; detectChanges(): void }) {
  const [subject, password] = Array.from(el.querySelectorAll('input'));
  subject.value = 'ruokki';
  subject.dispatchEvent(new Event('input'));
  password.value = 'secret';
  password.dispatchEvent(new Event('input'));
  fixture.detectChanges();
  el.querySelector('form')!.dispatchEvent(new Event('submit'));
  await fixture.whenStable();
  fixture.detectChanges();
}

describe('Connexion', () => {
  // Le lien partagé ne doit pas se perdre : se connecter ramène là où
  // `authGuard` avait intercepté.
  it('revient à l’URL demandée après connexion', async () => {
    const { auth, router, fixture, el } = monter(() => Promise.resolve({ subject: 'ruokki' }));
    await soumettre(el, fixture);

    expect(auth.connexion).toHaveBeenCalledWith('ruokki', 'secret');
    expect(router.navigateByUrl).toHaveBeenCalledWith('/runs/abc');
  });

  // Le serveur ne dit pas lequel des deux champs est faux, et l'écran non plus.
  it('rend le message du serveur tel quel, sans désigner de champ', async () => {
    const { router, fixture, el } = monter(() =>
      Promise.reject({ status: 401, message: 'invalid credentials' }),
    );
    await soumettre(el, fixture);

    expect(router.navigateByUrl).not.toHaveBeenCalled();
    expect(el.querySelector('[role="alert"]')?.textContent).toContain('invalid credentials');
    expect(el.querySelector('.is-invalid')).toBeNull();
  });

  it('retombe sur un message générique quand le serveur n’en donne pas', async () => {
    const { fixture, el } = monter(() => Promise.reject(new Error('')));
    await soumettre(el, fixture);

    expect(el.querySelector('[role="alert"]')?.textContent).toContain('sign-in failed');
  });
});

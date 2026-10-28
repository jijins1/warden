import { TestBed } from '@angular/core/testing';
import { Router } from '@angular/router';
import { describe, expect, it, vi } from 'vitest';
import { ChangementMotDePasse } from './changement-mot-de-passe';
import { AuthService } from '../../../../../core/services/auth/auth.service';

function monter(changerMotDePasse: () => Promise<void>) {
  const auth = { changerMotDePasse: vi.fn(changerMotDePasse) };
  const router = { navigate: vi.fn(() => Promise.resolve(true)) };
  TestBed.configureTestingModule({
    providers: [
      { provide: AuthService, useValue: auth },
      { provide: Router, useValue: router },
    ],
  });
  const fixture = TestBed.createComponent(ChangementMotDePasse);
  fixture.detectChanges();
  return { auth, router, fixture, el: fixture.nativeElement as HTMLElement };
}

async function soumettre(el: HTMLElement, fixture: { whenStable(): Promise<unknown>; detectChanges(): void }) {
  const [actuel, nouveau] = Array.from(el.querySelectorAll('input'));
  actuel.value = 'ancien';
  actuel.dispatchEvent(new Event('input'));
  nouveau.value = 'nouveau-mot-de-passe';
  nouveau.dispatchEvent(new Event('input'));
  fixture.detectChanges();
  el.querySelector('form')!.dispatchEvent(new Event('submit'));
  await fixture.whenStable();
  fixture.detectChanges();
}

describe('ChangementMotDePasse', () => {
  // Le succès déconnecte : l'écran de connexion doit dire que c'est voulu.
  it('renvoie à la connexion en disant pourquoi', async () => {
    const { auth, router, fixture, el } = monter(() => Promise.resolve());
    await soumettre(el, fixture);

    expect(auth.changerMotDePasse).toHaveBeenCalledWith('ancien', 'nouveau-mot-de-passe');
    expect(router.navigate).toHaveBeenCalledWith(['/login'], {
      queryParams: { raison: 'password-changed' },
    });
  });

  // Le 400 porte la règle de robustesse : elle doit arriver telle quelle.
  it('rend la règle du serveur telle quelle', async () => {
    const { router, fixture, el } = monter(() =>
      Promise.reject({ status: 400, message: 'at least 12 characters' }),
    );
    await soumettre(el, fixture);

    expect(router.navigate).not.toHaveBeenCalled();
    expect(el.querySelector('[role="alert"]')?.textContent).toContain('at least 12 characters');
  });
});

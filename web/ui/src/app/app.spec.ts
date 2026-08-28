import { TestBed } from '@angular/core/testing';
import { provideRouter } from '@angular/router';
import { describe, expect, it } from 'vitest';
import { App } from './app';

describe('App', () => {
  // La racine ne porte plus rien d'autre que l'exutoire : tout l'écran vient du
  // routeur, coquille comprise. Ce test tient cette propriété — y remettre du
  // contenu sortirait la coquille de la garde qui la protège.
  it('ne rend que l’exutoire de routes', async () => {
    await TestBed.configureTestingModule({
      imports: [App],
      providers: [provideRouter([])],
    }).compileComponents();

    const fixture = TestBed.createComponent(App);
    await fixture.whenStable();

    const racine = fixture.nativeElement as HTMLElement;
    expect(racine.querySelector('router-outlet')).not.toBeNull();
    expect(racine.textContent?.trim()).toBe('');
  });
});

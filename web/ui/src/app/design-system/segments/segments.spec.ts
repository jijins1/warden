import { Component } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { describe, expect, it } from 'vitest';
import { Segments } from './segments';

@Component({
  imports: [Segments],
  template: `<w-segments libelle="State" [options]="options" [actif]="actif" (choix)="choisi = $event" />`,
})
class Hote {
  options = ['running', 'failed'];
  actif: string | null = 'failed';
  choisi: string | null | undefined = undefined;
}

describe('Segments', () => {
  it('marque l’option active et émet celle qu’on choisit', async () => {
    const fixture = TestBed.createComponent(Hote);
    await fixture.whenStable();
    const boutons = (fixture.nativeElement as HTMLElement).querySelectorAll('button');
    expect(boutons[1].getAttribute('aria-pressed')).toBe('true');
    expect(boutons[0].getAttribute('aria-pressed')).toBe('false');

    boutons[0].click();
    expect(fixture.componentInstance.choisi).toBe('running');
  });

  it('recliquer l’option active la désélectionne', async () => {
    const fixture = TestBed.createComponent(Hote);
    await fixture.whenStable();
    (fixture.nativeElement as HTMLElement).querySelectorAll('button')[1].click();
    expect(fixture.componentInstance.choisi).toBeNull();
  });
});

import { Component } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { describe, expect, it } from 'vitest';
import { ChangedCount } from './changed-count';

@Component({ imports: [ChangedCount], template: '<w-changed-count [count]="count" />' })
class Hote {
  count = 0;
}

async function rendre(count: number) {
  const fixture = TestBed.createComponent(Hote);
  fixture.componentInstance.count = count;
  await fixture.whenStable();
  return (fixture.nativeElement as HTMLElement).querySelector('.w-count')!;
}

describe('ChangedCount', () => {
  it('efface un zéro et fait ressortir un non-zéro', async () => {
    expect((await rendre(0)).classList.contains('is-changed')).toBe(false);
    expect((await rendre(3)).classList.contains('is-changed')).toBe(true);
  });
});

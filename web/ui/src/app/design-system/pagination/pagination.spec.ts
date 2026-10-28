import { Component } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { describe, expect, it } from 'vitest';
import { Pagination } from './pagination';

@Component({
  imports: [Pagination],
  template: '<w-pagination [reste]="reste" [chargement]="chargement" (suite)="suites = suites + 1" />',
})
class Hote {
  reste = true;
  chargement = false;
  suites = 0;
}

describe('Pagination', () => {
  it('émet la suite au clic, et dit la fin quand le curseur manque', async () => {
    const fixture = TestBed.createComponent(Hote);
    await fixture.whenStable();
    const el = fixture.nativeElement as HTMLElement;
    el.querySelector('button')!.click();
    expect(fixture.componentInstance.suites).toBe(1);

    fixture.componentInstance.reste = false;
    fixture.changeDetectorRef.markForCheck();
    await fixture.whenStable();
    expect(el.textContent).toContain('End of results');
  });
});

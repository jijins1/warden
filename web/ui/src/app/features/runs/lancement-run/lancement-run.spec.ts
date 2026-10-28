import { TestBed } from '@angular/core/testing';
import { provideRouter, withComponentInputBinding } from '@angular/router';
import { RouterTestingHarness } from '@angular/router/testing';
import { describe, expect, it, vi } from 'vitest';
import { LancementRun } from './lancement-run';
import { RunsService } from '../../../core/services/runs/runs.service';

describe('LancementRun', () => {
  it('préremplit le formulaire depuis les paramètres de requête', async () => {
    TestBed.configureTestingModule({
      providers: [
        provideRouter([{ path: 'runs/new', component: LancementRun }], withComponentInputBinding()),
        { provide: RunsService, useValue: { lancer: vi.fn() } },
      ],
    });
    const harness = await RouterTestingHarness.create();
    await harness.navigateByUrl('/runs/new?repo=depot&environment=prod&playbook=site.yml');
    harness.detectChanges();
    await harness.fixture.whenStable();

    const valeur = (id: string) =>
      (harness.routeNativeElement!.querySelector(`#${id}`) as HTMLInputElement).value;
    expect(valeur('lancement-repo')).toBe('depot');
    expect(valeur('lancement-environment')).toBe('prod');
    expect(valeur('lancement-playbook')).toBe('site.yml');
    expect(valeur('lancement-inventory')).toBe('');
  });
});

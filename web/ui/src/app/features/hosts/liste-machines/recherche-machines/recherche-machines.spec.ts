import { Component, signal } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { Router, provideRouter } from '@angular/router';
import { describe, expect, it, vi } from 'vitest';
import { RechercheMachines } from './recherche-machines';
import { FiltreMachines, MachinesService } from '../../../../core/services/machines/machines.service';
import { EnvironmentsService } from '../../../../core/services/environments/environments.service';
import { ProtectedEnvironmentsService } from '../../../../core/services/protected-environments/protected-environments.service';
import { MachineEnrolee, PageMachines } from '../../../../core/models/wire';

@Component({
  imports: [RechercheMachines],
  template: '<w-recherche-machines [filtre]="filtre()" />',
})
class Hote {
  readonly filtre = signal<FiltreMachines>({});
}

function machine(host: string, extra: Partial<MachineEnrolee> = {}): MachineEnrolee {
  return {
    host,
    environment: 'prod',
    address: host,
    login: 'ansible',
    host_key: 'SHA256:x',
    mode: 'assisted',
    enrolled_at: '2026-10-28T11:31:21Z',
    ...extra,
  };
}

function page(hosts: MachineEnrolee[], suite = ''): PageMachines {
  return { hosts, next_cursor: suite };
}

async function monter(premiere: PageMachines = page([machine('db-01')])) {
  const machines = { lister: vi.fn().mockResolvedValue(premiere) };
  TestBed.configureTestingModule({
    providers: [
      provideRouter([]),
      { provide: MachinesService, useValue: machines },
      { provide: EnvironmentsService, useValue: { mesEnvironnements: () => Promise.resolve(['prod']) } },
      { provide: ProtectedEnvironmentsService, useValue: { charger: vi.fn(), isProtected: () => false } },
    ],
  });
  const navigate = vi.spyOn(TestBed.inject(Router), 'navigate').mockResolvedValue(true);
  const fixture = TestBed.createComponent(Hote);
  await fixture.whenStable();
  const el = fixture.nativeElement as HTMLElement;
  return { machines, navigate, fixture, el };
}

function bouton(el: HTMLElement, texte: string): HTMLButtonElement {
  const trouve = Array.from(el.querySelectorAll('button')).find(
    (b) => b.textContent?.trim() === texte,
  );
  if (!trouve) throw new Error(`pas de bouton « ${texte} »`);
  return trouve;
}

describe('RechercheMachines', () => {
  it('recharge quand les filtres changent, sans garder la page précédente', async () => {
    const { machines, fixture, el } = await monter();
    expect(machines.lister).toHaveBeenCalledWith({}, undefined);

    machines.lister.mockResolvedValue(page([machine('web-01'), machine('web-02')]));
    fixture.componentInstance.filtre.set({ q: 'web' });
    await fixture.whenStable();

    expect(machines.lister).toHaveBeenLastCalledWith({ q: 'web' }, undefined);
    expect(el.textContent).toContain('2 shown');
    expect(el.textContent).not.toContain('db-01');
  });

  it('« Load more » passe le curseur et ajoute à la suite', async () => {
    const { machines, fixture, el } = await monter();
    machines.lister.mockResolvedValue(page([machine('db-01')], 'curseur-1'));
    fixture.componentInstance.filtre.set({ env: 'prod' });
    await fixture.whenStable();

    machines.lister.mockResolvedValue(page([machine('db-02')]));
    bouton(el, 'Load more').click();
    await fixture.whenStable();

    expect(machines.lister).toHaveBeenLastCalledWith({ env: 'prod' }, 'curseur-1');
    expect(el.textContent).toContain('2 shown');
    expect(el.textContent).toContain('End of results');
  });

  it('chaque ligne mène à sa machine, et dit son adresse et son mode', async () => {
    const { machines, fixture, el } = await monter();
    machines.lister.mockResolvedValue(
      page([
        machine('db-01', { address: '10.0.0.5', port: 2222, mode: 'declared' }),
        machine('db-02', { address: '10.0.0.6', port: 22 }),
      ]),
    );
    fixture.componentInstance.filtre.set({ env: 'prod' });
    await fixture.whenStable();

    const [premiere, seconde] = Array.from(el.querySelectorAll<HTMLAnchorElement>('a.w-tr'));
    expect(premiere.getAttribute('href')).toBe('/hosts/db-01');
    expect(premiere.textContent).toContain('10.0.0.5:2222');
    expect(premiere.textContent).toContain('Declared');
    // Le port 22 est le défaut : l'afficher n'apprendrait rien.
    expect(seconde.textContent).toContain('10.0.0.6');
    expect(seconde.textContent).not.toContain(':22');
    expect(seconde.textContent).toContain('Verified');
  });

  it('écrit les filtres dans l’URL au lieu de recharger lui-même', async () => {
    const { machines, navigate, el } = await monter();
    machines.lister.mockClear();

    bouton(el, 'prod').click();
    expect(navigate).toHaveBeenCalledWith([], {
      queryParams: { env: 'prod' },
      queryParamsHandling: 'merge',
    });
    expect(machines.lister).not.toHaveBeenCalled();
  });

  it('« Clear filters » retire tous les filtres de l’URL', async () => {
    const { navigate, fixture, el } = await monter();
    fixture.componentInstance.filtre.set({ env: 'prod', q: 'db' });
    await fixture.whenStable();

    expect(el.textContent).toContain('q:db');
    bouton(el, 'Clear filters').click();
    expect(navigate).toHaveBeenCalledWith([], {
      queryParams: { env: null, q: null },
      queryParamsHandling: 'merge',
    });
  });

  it('sans machine ni filtre, dit comment en ajouter une', async () => {
    const { el } = await monter(page([]));

    expect(el.querySelector('.w-empty')?.textContent).toContain('No machine matches.');
    expect(el.querySelector('.w-empty')?.textContent).toContain('warden enroll');
  });

  it('montre l’erreur du serveur avec « Retry »', async () => {
    const { machines, fixture, el } = await monter();
    machines.lister.mockRejectedValue({ message: 'boom' });
    fixture.componentInstance.filtre.set({ q: 'x' });
    await fixture.whenStable();

    expect(el.querySelector('[role="alert"]')?.textContent).toContain('boom');
    machines.lister.mockResolvedValue(page([]));
    bouton(el, 'Retry').click();
    await fixture.whenStable();
    expect(el.querySelector('[role="alert"]')).toBeNull();
    expect(el.textContent).toContain('No machine matches.');
  });
});

import { TestBed } from '@angular/core/testing';
import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { MachinesService } from './machines.service';
import { APP_CONFIG } from '../../app-config/app-config';

describe('MachinesService', () => {
  let service: MachinesService;
  let ctrl: HttpTestingController;

  beforeEach(() => {
    TestBed.configureTestingModule({
      providers: [
        provideHttpClient(),
        provideHttpClientTesting(),
        { provide: APP_CONFIG, useValue: { apiUrl: 'https://api.test/' } },
      ],
    });
    service = TestBed.inject(MachinesService);
    ctrl = TestBed.inject(HttpTestingController);
  });

  afterEach(() => ctrl.verify());

  it('passe les filtres posés et le curseur, et rend la page', async () => {
    const page = service.lister({ env: 'prod', q: 'db' }, 'curseur-1');
    const req = ctrl.expectOne((r) => r.url === 'https://api.test/hosts');
    expect(req.request.params.get('env')).toBe('prod');
    expect(req.request.params.get('q')).toBe('db');
    expect(req.request.params.get('cursor')).toBe('curseur-1');
    req.flush({ hosts: [], next_cursor: '' });

    expect(await page).toEqual({ hosts: [], next_cursor: '' });
  });

  it("n'envoie ni filtre vide ni curseur absent", () => {
    void service.lister({ env: undefined, q: '' });
    const req = ctrl.expectOne((r) => r.url === 'https://api.test/hosts');
    expect(req.request.params.keys()).toEqual([]);
    req.flush({ hosts: [], next_cursor: '' });
  });
});

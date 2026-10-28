import { TestBed } from '@angular/core/testing';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { FluxRun } from './flux-run';
import { APP_CONFIG } from '../../../../../core/app-config/app-config';

/** Une socket qui ne se connecte jamais : on ne regarde que son ouverture et sa fermeture. */
class FausseSocket {
  static ouvertes: FausseSocket[] = [];
  readonly close = vi.fn();
  constructor(readonly url: string) {
    FausseSocket.ouvertes.push(this);
  }
}

function monter() {
  FausseSocket.ouvertes = [];
  vi.stubGlobal('WebSocket', FausseSocket);
  TestBed.configureTestingModule({
    providers: [{ provide: APP_CONFIG, useValue: { apiUrl: 'https://api.invalid/' } }],
  });
  const fixture = TestBed.createComponent(FluxRun);
  fixture.componentRef.setInput('runRef', '01J');
  return fixture;
}

describe('FluxRun', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('ouvre le flux du run depuis l’offset 0', async () => {
    const fixture = monter();
    await fixture.whenStable();

    expect(FausseSocket.ouvertes.map((s) => s.url)).toEqual([
      'wss://api.invalid/runs/01J/stream?from_offset=0',
    ]);
  });

  // Quitter la page doit fermer la socket : sans cela, son réessai tournerait
  // en fond, et un onglet longtemps ouvert accumulerait une connexion par run.
  it('ferme le flux avec la vue', async () => {
    const fixture = monter();
    await fixture.whenStable();

    fixture.destroy();

    expect(FausseSocket.ouvertes[0].close).toHaveBeenCalled();
  });

  it('ferme l’ancien flux quand la référence change', async () => {
    const fixture = monter();
    await fixture.whenStable();

    fixture.componentRef.setInput('runRef', '01K');
    await fixture.whenStable();

    expect(FausseSocket.ouvertes[0].close).toHaveBeenCalled();
    expect(FausseSocket.ouvertes[1].url).toContain('/runs/01K/stream');
  });
});

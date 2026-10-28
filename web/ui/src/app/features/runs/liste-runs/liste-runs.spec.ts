import { describe, expect, it } from 'vitest';
import { filtreDepuisUrl } from './liste-runs';

describe('filtreDepuisUrl', () => {
  it('ne garde que les filtres que le serveur applique, typés', () => {
    expect(
      filtreDepuisUrl({ env: 'prod', state: 'failed', changed_min: '1', mine: '1' }),
    ).toEqual({
      env: 'prod',
      inventory: undefined,
      state: 'failed',
      since: undefined,
      until: undefined,
      changed_min: 1,
      mine: 1,
    });
  });

  it('écarte un changed_min qui n’est pas un nombre positif', () => {
    expect(filtreDepuisUrl({ changed_min: 'abc' }).changed_min).toBeUndefined();
    expect(filtreDepuisUrl({ changed_min: '0' }).changed_min).toBeUndefined();
    expect(filtreDepuisUrl({ changed_min: '-2' }).changed_min).toBeUndefined();
  });

  it('mine ne vaut que pour « 1 »', () => {
    expect(filtreDepuisUrl({ mine: 'true' }).mine).toBeUndefined();
  });
});

import { describe, expect, it, vi, afterEach } from 'vitest';
import { DureePipe } from './duree.pipe';

const duree = new DureePipe();

describe('DureePipe', () => {
  afterEach(() => vi.useRealTimers());

  it('formate en millisecondes, secondes, minutes et heures', () => {
    const t0 = '2026-08-27T10:00:00.000Z';
    expect(duree.transform(t0, '2026-08-27T10:00:00.418Z')).toBe('418ms');
    expect(duree.transform(t0, '2026-08-27T10:00:12.400Z')).toBe('12.4s');
    expect(duree.transform(t0, '2026-08-27T10:01:04.000Z')).toBe('1m 04s');
    expect(duree.transform(t0, '2026-08-27T12:05:00.000Z')).toBe('2h 05m');
  });

  // LE test. Une fin absente ne vaut PAS zéro : le run court encore, et
  // afficher « 0s » sur un run en vol énoncerait un fait faux.
  it('mesure jusqu’à maintenant quand le run court encore', () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-08-27T10:00:30.000Z'));

    expect(duree.transform('2026-08-27T10:00:00.000Z')).toBe('30.0s');
    expect(duree.transform('2026-08-27T10:00:00.000Z', null)).toBe('30.0s');
  });

  it('rend un tiret sur une date illisible plutôt qu’un NaN', () => {
    expect(duree.transform('pas une date')).toBe('—');
    expect(duree.transform('2026-08-27T10:00:00.000Z', 'pas une date')).toBe('—');
  });
});

import { describe, expect, it } from 'vitest';
import { sortieDe } from './event-stream';

describe('sortieDe', () => {
  // C'EST la raison d'être de la fonction : le `stdout` d'un event est déjà en
  // base et déjà transmis au navigateur, mais rien ne l'affichait. Un run rouge
  // ne disait donc pas POURQUOI.
  it('rend la sortie quand elle est présente', () => {
    expect(sortieDe({ stdout: 'PLAY [essai] ***' })).toBe('PLAY [essai] ***');
  });

  // Le payload vient d'un module Ansible, donc de la machine gérée : il n'est
  // pas de forme garantie. Tout ce qui n'est pas une chaîne utile rend vide,
  // plutôt que d'afficher « [object Object] » ou de lever.
  it('rend vide sur tout ce qui n’est pas une sortie', () => {
    for (const cas of [null, undefined, 42, 'brut', {}, { stdout: 123 }, { stdout: '' }]) {
      expect(sortieDe(cas)).toBe('');
    }
  });

  // Les blancs de fin sont retirés : ansible-runner termine ses lignes par des
  // retours chariot, qui feraient afficher un bloc à moitié vide.
  it('élague les blancs de fin', () => {
    expect(sortieDe({ stdout: 'ok\r\n\r\n' })).toBe('ok');
  });
});

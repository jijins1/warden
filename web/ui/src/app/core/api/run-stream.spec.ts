import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { RunStream, versWebSocket } from './run-stream';

/** Socket contrôlée : on décide quand elle s'ouvre, parle et tombe. */
class FauxSocket {
  static ouvertes: FauxSocket[] = [];

  onopen: (() => void) | null = null;
  onmessage: ((ev: MessageEvent<string>) => void) | null = null;
  onerror: (() => void) | null = null;
  onclose: (() => void) | null = null;
  fermee = false;

  constructor(readonly url: string) {
    FauxSocket.ouvertes.push(this);
  }

  ouvrir(): void {
    this.onopen?.();
  }

  emettre(msg: unknown): void {
    this.onmessage?.({ data: JSON.stringify(msg) } as MessageEvent<string>);
  }

  emettreBrut(brut: string): void {
    this.onmessage?.({ data: brut } as MessageEvent<string>);
  }

  tomber(): void {
    this.fermee = true;
    this.onclose?.();
  }

  close(): void {
    this.fermee = true;
  }
}

const event = (seq: number) => ({
  type: 'event',
  body: { seq, occurred_at: '2026-08-27T10:00:00Z', state: 'ok', payload: {} },
});

function flux(): { s: RunStream; sockets: FauxSocket[] } {
  const sockets = FauxSocket.ouvertes;
  const s = new RunStream(
    'wss://api.example.com/runs/01J/stream',
    (url) => new FauxSocket(url) as unknown as WebSocket,
  );
  return { s, sockets };
}

describe('RunStream', () => {
  beforeEach(() => {
    FauxSocket.ouvertes = [];
    vi.useFakeTimers();
  });
  afterEach(() => vi.useRealTimers());

  it('demande l’historique complet à la première connexion', () => {
    const { s, sockets } = flux();
    s.demarrer();
    expect(sockets[0].url).toContain('from_offset=0');
  });

  // LE test de ce fichier. Le serveur ne garde AUCUNE trace de ce qu'un client
  // déconnecté avait reçu : l'offset vient du client. Sans reprise, chaque
  // coupure perdrait des events en silence.
  it('reprend au seq suivant après une coupure', () => {
    const { s, sockets } = flux();
    s.demarrer();
    sockets[0].ouvrir();
    sockets[0].emettre(event(41));
    sockets[0].emettre(event(42));
    sockets[0].tomber();

    expect(s.etat()).toBe('reconnecting');
    vi.advanceTimersByTime(500);

    expect(sockets).toHaveLength(2);
    expect(sockets[1].url).toContain('from_offset=43');
  });

  // La marque n'apparaît qu'à une REPRISE, jamais à la première ouverture : en
  // poser une au départ ferait croire à une coupure qui n'a pas eu lieu.
  it('marque la reprise, et seulement la reprise', () => {
    const { s, sockets } = flux();
    s.demarrer();
    sockets[0].ouvrir();
    expect(s.lignes().filter((l) => l.kind === 'resume')).toHaveLength(0);

    sockets[0].emettre(event(7));
    sockets[0].tomber();
    vi.advanceTimersByTime(500);
    sockets[1].ouvrir();

    const marques = s.lignes().filter((l) => l.kind === 'resume');
    expect(marques).toHaveLength(1);
    expect(marques[0]).toMatchObject({ kind: 'resume', seq: 8 });
  });

  // Si le serveur reprend un cran trop tôt, un event déjà affiché reviendrait.
  // Le `seq` est monotone par run : il tranche.
  it('écarte les doublons sur seq', () => {
    const { s, sockets } = flux();
    s.demarrer();
    sockets[0].ouvrir();
    sockets[0].emettre(event(1));
    sockets[0].emettre(event(2));
    sockets[0].emettre(event(2));
    sockets[0].emettre(event(1));

    expect(s.lignes().filter((l) => l.kind === 'event')).toHaveLength(2);
  });

  // Un run clos ne se rouvre pas : réessayer indéfiniment sur un flux terminé
  // martèlerait le serveur pour rien.
  it('ne se reconnecte pas après le cadre de fin', () => {
    const { s, sockets } = flux();
    s.demarrer();
    sockets[0].ouvrir();
    sockets[0].emettre({ type: 'end', body: { status: 'succeeded' } });

    expect(s.etat()).toBe('ended');
    expect(s.fin()).toMatchObject({ status: 'succeeded' });

    sockets[0].tomber();
    vi.advanceTimersByTime(30000);
    expect(sockets).toHaveLength(1);
  });

  it('porte la cause d’un run interrompu jusqu’à la vue', () => {
    const { s, sockets } = flux();
    s.demarrer();
    sockets[0].ouvrir();
    sockets[0].emettre({
      type: 'end',
      body: { status: 'interrupted', reason: 'user_interrupt' },
    });

    expect(s.fin()).toMatchObject({ status: 'interrupted', reason: 'user_interrupt' });
  });

  // Un octet de travers ne doit pas coûter le reste du flux.
  it('survit à un cadre illisible', () => {
    const { s, sockets } = flux();
    s.demarrer();
    sockets[0].ouvrir();
    sockets[0].emettreBrut('{ pas du json');
    sockets[0].emettre(event(1));

    expect(s.lignes().filter((l) => l.kind === 'event')).toHaveLength(1);
    expect(s.etat()).toBe('live');
  });

  it('ignore le keepalive sans le compter comme un event', () => {
    const { s, sockets } = flux();
    s.demarrer();
    sockets[0].ouvrir();
    sockets[0].emettre({ type: 'keepalive', body: { timestamp: '2026-08-27T10:00:00Z' } });

    expect(s.lignes()).toHaveLength(0);
    expect(s.etat()).toBe('live');
  });

  it('espace les réessais au lieu de marteler', () => {
    const { s, sockets } = flux();
    s.demarrer();
    sockets[0].ouvrir();
    sockets[0].tomber();

    vi.advanceTimersByTime(499);
    expect(sockets).toHaveLength(1);
    vi.advanceTimersByTime(1);
    expect(sockets).toHaveLength(2);

    // Deuxième échec : le palier suivant est plus long.
    sockets[1].tomber();
    vi.advanceTimersByTime(999);
    expect(sockets).toHaveLength(2);
    vi.advanceTimersByTime(1);
    expect(sockets).toHaveLength(3);
  });

  it('cesse tout réessai une fois arrêté', () => {
    const { s, sockets } = flux();
    s.demarrer();
    sockets[0].ouvrir();
    s.arreter();
    sockets[0].tomber();

    vi.advanceTimersByTime(30000);
    expect(sockets).toHaveLength(1);
  });
});

describe('versWebSocket', () => {
  // Rétrograder une origine chiffrée enverrait le cookie en clair — et un
  // cookie `Secure` ne partirait de toute façon pas.
  it('conserve le chiffrement', () => {
    expect(versWebSocket('https://warden.example.com')).toBe('wss://warden.example.com');
    expect(versWebSocket('http://localhost:8080')).toBe('ws://localhost:8080');
  });

  // MÊME ORIGINE : `apiUrl` devient un chemin, plus une origine. Laisser le
  // navigateur résoudre marcherait, mais la garantie du `s` ne serait plus tenue
  // par ce code — elle serait déléguée. On la garde explicite.
  it('résout un chemin relatif sur l’origine de la page', () => {
    expect(versWebSocket('/api', 'https://warden.example.com')).toBe(
      'wss://warden.example.com/api',
    );
    expect(versWebSocket('/api', 'http://localhost:4200')).toBe('ws://localhost:4200/api');
  });

  // Une origine absolue reste prioritaire : un déploiement à deux hôtes doit
  // continuer de fonctionner sans changer une ligne.
  it('ignore l’origine de la page quand la base est absolue', () => {
    expect(versWebSocket('https://api.example.com', 'https://ui.example.com')).toBe(
      'wss://api.example.com',
    );
  });
});

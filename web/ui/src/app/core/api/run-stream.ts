import { signal } from '@angular/core';
import { EndFrame, ErrorFrame, EventFrame, Message } from './wire-ws';

export type EtatFlux = 'connecting' | 'live' | 'reconnecting' | 'ended' | 'error';

/**
 * Une ligne du flux : un event, ou la marque d'une reprise.
 *
 * Les deux dans la MÊME liste pour que la reprise s'affiche à sa place
 * chronologique. La reléguer dans un bandeau la détacherait des events qu'elle
 * concerne, et c'est justement leur voisinage qui prouve que rien ne manque.
 */
export type LigneFlux =
  | { readonly kind: 'event'; readonly event: EventFrame }
  | { readonly kind: 'resume'; readonly seq: number };

/** Paliers de réessai, en millisecondes. Plafonnés pour ne pas abandonner. */
const PALIERS = [500, 1000, 2000, 4000, 8000, 15000];

/**
 * Le flux d'events d'un run.
 *
 * LA propriété tenue ici est la reprise par offset. Une connexion WebSocket
 * s'épingle à un réplica de l'API ; un rolling update, un proxy ou un portable
 * en veille la coupent. Le serveur ne garde AUCUNE trace de ce qu'un client
 * déconnecté avait reçu — l'offset est fourni par le client (core/ports.go).
 * Sans reprise, chaque coupure perdrait des events en silence, ce qui est la
 * défaillance qu'un produit d'audit ne peut pas se permettre.
 *
 * Reprendre est sûr parce que `Follow` n'a pas deux chemins : il LIT LA TABLE
 * depuis l'offset demandé, et `NOTIFY` ne fait que dire quand relire (ADR-0008).
 * Le rattrapage et le direct sont le même mécanisme.
 *
 * Aucun jeton n'est transmis : le navigateur attache le cookie de session au
 * handshake tout seul. C'est la raison d'être du cookie — l'API WebSocket ne
 * permet pas de poser un en-tête — et c'est aussi pourquoi l'ADR-0035 exige que
 * l'UI et l'API partagent un domaine enregistrable.
 */
export class RunStream {
  readonly #lignes = signal<LigneFlux[]>([]);
  readonly #etat = signal<EtatFlux>('connecting');
  readonly #fin = signal<EndFrame | null>(null);
  readonly #erreur = signal<string | null>(null);

  readonly lignes = this.#lignes.asReadonly();
  readonly etat = this.#etat.asReadonly();
  readonly fin = this.#fin.asReadonly();
  readonly erreur = this.#erreur.asReadonly();

  #socket: WebSocket | null = null;
  #dernierSeq = -1;
  #essai = 0;
  #minuteur: ReturnType<typeof setTimeout> | null = null;
  #arrete = false;
  /** Vrai dès qu'une coupure a eu lieu : la prochaine ouverture est une reprise. */
  #reprise = false;

  constructor(
    private readonly url: string,
    private readonly ouvrir: (url: string) => WebSocket = (u) => new WebSocket(u),
  ) {}

  demarrer(): void {
    this.#arrete = false;
    this.#connecter();
  }

  /** Ferme sans réessayer. À appeler quand la vue disparaît. */
  arreter(): void {
    this.#arrete = true;
    if (this.#minuteur) {
      clearTimeout(this.#minuteur);
      this.#minuteur = null;
    }
    this.#socket?.close();
    this.#socket = null;
  }

  #connecter(): void {
    if (this.#arrete) {
      return;
    }
    // `from_offset` = le suivant de ce qu'on a. À la première connexion c'est 0,
    // donc l'historique complet : c'est ce qui fait qu'un run TERMINÉ se relit
    // par le même chemin qu'un run en vol.
    const depuis = this.#dernierSeq + 1;
    const separateur = this.url.includes('?') ? '&' : '?';
    const socket = this.ouvrir(`${this.url}${separateur}from_offset=${depuis}`);
    this.#socket = socket;

    socket.onopen = () => {
      this.#etat.set('live');
      this.#essai = 0;
      if (this.#reprise) {
        this.#reprise = false;
        this.#lignes.update((l) => [...l, { kind: 'resume', seq: depuis }]);
      }
    };

    socket.onmessage = (ev: MessageEvent<string>) => this.#recevoir(ev.data);

    socket.onerror = () => {
      // Rien ici : `onclose` suit toujours, et traiter les deux dupliquerait le
      // réessai.
    };

    socket.onclose = () => {
      this.#socket = null;
      if (this.#arrete || this.#etat() === 'ended') {
        return;
      }
      this.#reessayer();
    };
  }

  #recevoir(brut: string): void {
    let msg: Message;
    try {
      msg = JSON.parse(brut) as Message;
    } catch {
      // Un cadre illisible n'interrompt pas le flux : le suivant sera peut-être
      // bon, et couper ici perdrait des events pour un octet de travers.
      return;
    }

    switch (msg.type) {
      case 'event': {
        const e = msg.body as EventFrame;
        // Les doublons sont possibles à la reprise si le serveur reprend un cran
        // trop tôt. On les écarte sur `seq`, qui est monotone par run.
        if (e.seq <= this.#dernierSeq) {
          return;
        }
        this.#dernierSeq = e.seq;
        this.#lignes.update((l) => [...l, { kind: 'event', event: e }]);
        break;
      }
      case 'end': {
        this.#fin.set(msg.body as EndFrame);
        this.#etat.set('ended');
        this.#arrete = true;
        this.#socket?.close();
        break;
      }
      case 'error': {
        this.#erreur.set((msg.body as ErrorFrame).error);
        this.#etat.set('error');
        break;
      }
      case 'keepalive':
        // Rien à faire : sa seule fonction est d'empêcher un proxy de couper une
        // connexion oisive. Le recevoir prouve que le lien tient.
        break;
    }
  }

  #reessayer(): void {
    this.#reprise = true;
    this.#etat.set('reconnecting');
    const delai = PALIERS[Math.min(this.#essai, PALIERS.length - 1)];
    this.#essai += 1;
    this.#minuteur = setTimeout(() => this.#connecter(), delai);
  }
}

/**
 * `http://…` → `ws://…`, `https://…` → `wss://…`.
 *
 * Le `s` est CONSERVÉ : rétrograder une origine chiffrée en `ws://` enverrait le
 * cookie de session en clair, et un cookie `Secure` ne partirait de toute façon
 * pas — la connexion échouerait sans dire pourquoi.
 *
 * `base` peut être un CHEMIN (`/api`) quand l'API partage l'origine de
 * l'interface. Le navigateur saurait résoudre un tel chemin seul, mais la
 * garantie ci-dessus cesserait alors d'être tenue par ce code pour être
 * déléguée : on résout ici, explicitement, sur l'origine de la page.
 */
export function versWebSocket(base: string, origine = globalThis.location?.origin ?? ''): string {
  const absolue = /^https?:/i.test(base) ? base : origine.replace(/\/+$/, '') + base;
  return absolue.replace(/^https:/i, 'wss:').replace(/^http:/i, 'ws:');
}

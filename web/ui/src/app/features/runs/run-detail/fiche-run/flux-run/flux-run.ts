import {
  ChangeDetectionStrategy,
  Component,
  DestroyRef,
  effect,
  inject,
  input,
  signal,
  untracked,
} from '@angular/core';
import { MatDialog } from '@angular/material/dialog';
import { APP_CONFIG } from '../../../../../core/app-config/app-config';
import { RunStream, versWebSocket } from '../../../../../core/services/run-stream/run-stream';
import { EventFrame } from '../../../../../core/models/wire-ws';
import { EventStream } from './event-stream/event-stream';
import { DonneesDiffEvent, EventDiffDialog } from './event-diff-dialog/event-diff-dialog';
import { detailDe } from './detail-event/detail-event';

/**
 * Le flux d'events d'un run, en temps réel.
 *
 * Le container ouvre et ferme la socket (`RunStream`), passe ses lignes au flux
 * affiché, et ouvre le diff de l'event qu'on lui désigne.
 */
@Component({
  selector: 'w-flux-run',
  imports: [EventStream],
  templateUrl: './flux-run.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class FluxRun {
  readonly #wsBase = versWebSocket(inject(APP_CONFIG).apiUrl.replace(/\/+$/, ''));
  readonly #dialogue = inject(MatDialog);

  readonly runRef = input.required<string>();

  /** Le flux, créé une fois la référence connue et fermé avec la vue. */
  protected readonly flux = signal<RunStream | null>(null);

  constructor() {
    effect(() => {
      const ref = this.runRef();
      // `untracked` : sans lui, écrire `flux` depuis un effet qui le LIT
      // rouvrirait le flux en boucle.
      untracked(() => {
        this.flux()?.arreter();
        // Ouvert MÊME sur un run terminé : le serveur relit la table depuis
        // l'offset 0 et envoie l'historique puis le cadre de fin. Un run clos se
        // relit donc par le même chemin qu'un run en vol — il n'y a pas deux
        // codes à maintenir.
        const flux = new RunStream(`${this.#wsBase}/runs/${encodeURIComponent(ref)}/stream`);
        flux.demarrer();
        this.flux.set(flux);
      });
    });

    // Le flux est FERMÉ avec la vue. Sans cela, quitter la page laisserait la
    // socket ouverte et son réessai tourner en fond : un onglet longtemps
    // ouvert accumulerait une connexion par run consulté.
    inject(DestroyRef).onDestroy(() => this.flux()?.arreter());
  }

  /** Le payload `--diff` est une lecture SÉPARÉMENT autorisée, jamais servie
   * dans le flux : il vit dans une table à contrôle d'accès distinct parce
   * qu'il contient des secrets rendus. L'ouvrir est donc une action, pas un
   * dépliage. */
  protected ouvrirDiff(e: EventFrame): void {
    this.#dialogue.open<EventDiffDialog, DonneesDiffEvent>(EventDiffDialog, {
      panelClass: 'w-dialog-panel',
      backdropClass: 'w-scrim-backdrop',
      maxWidth: '680px',
      ariaLabelledBy: 'titre-event-diff',
      data: {
        runRef: this.runRef(),
        seq: e.seq,
        etat: e.state,
        tache: e.task_name,
        hote: e.host,
        detail: detailDe(e.payload),
      },
    });
  }
}

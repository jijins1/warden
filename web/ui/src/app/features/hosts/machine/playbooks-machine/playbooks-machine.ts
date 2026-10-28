import {
  ChangeDetectionStrategy,
  Component,
  computed,
  effect,
  inject,
  input,
  signal,
  untracked,
} from '@angular/core';
import { MatIconModule } from '@angular/material/icon';
import { RunsService } from '../../../../core/services/runs/runs.service';
import { PlaybookSummary } from '../../../../core/models/wire';
import { ApiError } from '../../../../core/models/api-error';
import { RunDiff } from '../../../run-diff/run-diff';
import { LignePlaybook, PanneauPlaybook, TablePlaybooks } from './table-playbooks/table-playbooks';

/**
 * Les playbooks qui ont tourné sur une machine dans la fenêtre (§6.5
 * dédupliqué), chacun avec le diff de son dernier run à la demande.
 */
@Component({
  selector: 'w-playbooks-machine',
  imports: [MatIconModule, TablePlaybooks, PanneauPlaybook, RunDiff],
  templateUrl: './playbooks-machine.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: { class: 'w-stack' },
})
export class PlaybooksMachine {
  readonly #runs = inject(RunsService);

  readonly host = input.required<string>();
  readonly since = input<string>();
  readonly until = input<string>();

  protected readonly lignes = signal<LignePlaybook[]>([]);
  protected readonly chargement = signal(false);
  protected readonly erreur = signal<string | null>(null);
  /** Faux tant que la première réponse n'est pas arrivée : distingue « vide » de « pas encore ». */
  protected readonly charge = signal(false);

  protected readonly vide = computed(
    () => this.charge() && !this.erreur() && this.lignes().length === 0,
  );

  constructor() {
    effect(() => {
      this.host();
      this.since();
      this.until();
      untracked(() => void this.charger());
    });
  }

  protected async charger(): Promise<void> {
    this.chargement.set(true);
    this.erreur.set(null);
    try {
      const playbooks = await this.#runs.playbooksParMachine(this.host(), this.since(), this.until());
      this.lignes.set(playbooks.map(versLigne));
    } catch (e: unknown) {
      this.erreur.set((e as Partial<ApiError>).message ?? 'listing playbooks failed');
    } finally {
      this.chargement.set(false);
      this.charge.set(true);
    }
  }
}

function versLigne(pb: PlaybookSummary): LignePlaybook {
  return {
    cle: `${pb.Repo}/${pb.Playbook}`,
    playbook: pb.Playbook,
    repo: pb.Repo,
    environnement: pb.Environment,
    inventaire: pb.Inventory,
    etat: pb.LastState,
    debut: pb.LastStartedAt,
    runRef: pb.LastRunRef,
    // Un run_ref ne se tronque jamais sous 10 caractères ; la valeur complète
    // reste au survol.
    runRefCourt: pb.LastRunRef.slice(0, 10),
  };
}

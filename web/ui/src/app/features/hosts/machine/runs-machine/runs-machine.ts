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
import { RunListItem } from '../../../../core/models/wire';
import { ApiError } from '../../../../core/models/api-error';
import { TableRuns } from '../../../table-runs/table-runs';

/** Le plafond serveur. Atteint, la réponse est peut-être incomplète. */
const PLAFOND = 500;

/** Tout ce qui a touché une machine dans la fenêtre, run par run. */
@Component({
  selector: 'w-runs-machine',
  imports: [MatIconModule, TableRuns],
  templateUrl: './runs-machine.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: { class: 'w-stack' },
})
export class RunsMachine {
  readonly #runs = inject(RunsService);

  readonly host = input.required<string>();
  readonly since = input<string>();
  readonly until = input<string>();

  protected readonly PLAFOND = PLAFOND;

  protected readonly runs = signal<RunListItem[]>([]);
  protected readonly chargement = signal(false);
  protected readonly erreur = signal<string | null>(null);
  /** Faux tant que la première réponse n'est pas arrivée : distingue « vide » de « pas encore ». */
  protected readonly charge = signal(false);

  protected readonly vide = computed(
    () => this.charge() && !this.erreur() && this.runs().length === 0,
  );
  protected readonly plafondAtteint = computed(() => this.runs().length >= PLAFOND);

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
      this.runs.set(await this.#runs.parMachine(this.host(), this.since(), this.until()));
    } catch (e: unknown) {
      this.erreur.set((e as Partial<ApiError>).message ?? 'listing runs failed');
    } finally {
      this.chargement.set(false);
      this.charge.set(true);
    }
  }
}

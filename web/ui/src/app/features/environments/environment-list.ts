import { ChangeDetectionStrategy, Component, inject, signal } from '@angular/core';
import { DatePipe } from '@angular/common';
import { MatIconModule } from '@angular/material/icon';
import { MatProgressBarModule } from '@angular/material/progress-bar';
import { MatButtonModule } from '@angular/material/button';
import { MatTooltipModule } from '@angular/material/tooltip';
import { EnvironmentsService } from '../../core/api/environments.service';
import { KeyDescriptor } from '../../core/api/wire';
import { ApiError } from '../../core/http/api-error';

/**
 * Les environnements connus, vus par leur clé d'accès.
 *
 * `GET /admin/keys` rend l'inventaire de clés — c'est ce qui fait qu'un
 * environnement EXISTE pour warden : sans clé, il n'y a rien à administrer à
 * son sujet (ADR-0020). La partie privée n'apparaît jamais ici, comme nulle
 * part ailleurs dans le produit.
 */
@Component({
  selector: 'w-environment-list',
  imports: [DatePipe, MatIconModule, MatProgressBarModule, MatButtonModule, MatTooltipModule],
  templateUrl: './environment-list.html',
  styleUrl: './environment-list.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class EnvironmentList {
  readonly #environments = inject(EnvironmentsService);

  protected readonly items = signal<KeyDescriptor[]>([]);
  protected readonly chargement = signal(false);
  protected readonly erreur = signal<string | null>(null);
  /** Faux tant que la première réponse n'est pas arrivée : distingue « vide » de « pas encore ». */
  protected readonly charge = signal(false);

  constructor() {
    void this.charger();
  }

  protected async charger(): Promise<void> {
    this.chargement.set(true);
    this.erreur.set(null);
    try {
      this.items.set(await this.#environments.lister());
    } catch (e: unknown) {
      const err = e as Partial<ApiError>;
      this.erreur.set(err?.message ?? 'listing environments failed');
    } finally {
      this.chargement.set(false);
      this.charge.set(true);
    }
  }
}

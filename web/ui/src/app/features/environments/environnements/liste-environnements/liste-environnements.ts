import { ChangeDetectionStrategy, Component, computed, inject, signal } from '@angular/core';
import { MatIconModule } from '@angular/material/icon';
import { EnvironmentsService } from '../../../../core/services/environments/environments.service';
import { KeyDescriptor } from '../../../../core/models/wire';
import { ApiError } from '../../../../core/models/api-error';
import { LigneEnvironnement, TableEnvironnements } from './table-environnements/table-environnements';

/** L'inventaire des clés d'accès par environnement, avec ses états de chargement. */
@Component({
  selector: 'w-liste-environnements',
  imports: [MatIconModule, TableEnvironnements],
  templateUrl: './liste-environnements.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: { class: 'w-stack' },
})
export class ListeEnvironnements {
  readonly #environments = inject(EnvironmentsService);

  protected readonly lignes = signal<LigneEnvironnement[]>([]);
  protected readonly chargement = signal(false);
  protected readonly erreur = signal<string | null>(null);
  /** Faux tant que la première réponse n'est pas arrivée : distingue « vide » de « pas encore ». */
  protected readonly charge = signal(false);

  protected readonly vide = computed(
    () => this.charge() && !this.erreur() && this.lignes().length === 0,
  );

  constructor() {
    void this.charger();
  }

  protected async charger(): Promise<void> {
    this.chargement.set(true);
    this.erreur.set(null);
    try {
      this.lignes.set((await this.#environments.lister()).map(versLigne));
    } catch (e: unknown) {
      const err = e as Partial<ApiError>;
      this.erreur.set(err?.message ?? 'listing environments failed');
    } finally {
      this.chargement.set(false);
      this.charge.set(true);
    }
  }
}

function versLigne(cle: KeyDescriptor): LigneEnvironnement {
  return {
    environnement: cle.environment,
    empreinte: cle.fingerprint,
    clePublique: cle.public_key,
    origine: cle.origin,
    creation: cle.created_at,
    rotation: cle.rotated_at ?? null,
  };
}

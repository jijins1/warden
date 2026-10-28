import { ChangeDetectionStrategy, Component, computed, input } from '@angular/core';
import { DatePipe } from '@angular/common';
import { MatIconModule } from '@angular/material/icon';

/** Le modèle de vue de `GET /auth/whoami`. */
export interface FaitsDuCompte {
  subject: string;
  nom: string | null;
  administrateur: boolean;
  /** `null` en mode de développement : il n'y a pas eu de connexion. */
  expiration: string | null;
  /** Vrai quand l'identité vient d'un en-tête de développement, pas d'une session. */
  identiteAffirmee: boolean;
}

/** Les faits du compte, et ce qu'ils n'impliquent pas. */
@Component({
  selector: 'w-faits-compte',
  imports: [DatePipe, MatIconModule],
  templateUrl: './faits-compte.html',
  host: { style: 'display: contents' },
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class FaitsCompte {
  readonly faits = input.required<FaitsDuCompte>();

  protected readonly administrateur = computed(() => (this.faits().administrateur ? 'yes' : 'no'));
}

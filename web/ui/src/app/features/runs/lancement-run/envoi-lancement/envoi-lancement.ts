import { ChangeDetectionStrategy, Component, inject, input, signal } from '@angular/core';
import { Router } from '@angular/router';
import { RunsService } from '../../../../core/services/runs/runs.service';
import { ApiError } from '../../../../core/models/api-error';
import { composerFlags } from './flags/flags';
import {
  FormulaireLancement,
  PreremplissageLancement,
  ValeursLancement,
} from './formulaire-lancement/formulaire-lancement';

/**
 * Envoie la demande de lancement, puis ouvre le run créé.
 *
 * Il reçoit le préremplissage, traduit la valeur du formulaire en `LancementRun`
 * et décide de la suite : le détail du run, ou le message du serveur.
 */
@Component({
  selector: 'w-envoi-lancement',
  imports: [FormulaireLancement],
  templateUrl: './envoi-lancement.html',
  host: { style: 'display: contents' },
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class EnvoiLancement {
  readonly #runs = inject(RunsService);
  readonly #router = inject(Router);

  readonly valeurInitiale = input<PreremplissageLancement | null>(null);

  protected readonly enCours = signal(false);
  protected readonly erreur = signal<string | null>(null);

  protected async lancer(valeurs: ValeursLancement): Promise<void> {
    if (this.enCours()) {
      return;
    }
    this.enCours.set(true);
    this.erreur.set(null);

    const { repo, environment, inventory, playbook, becomePassword, ...options } = valeurs;
    try {
      const cree = await this.#runs.lancer(repo, {
        environment,
        inventory: inventory || undefined,
        playbook,
        flags: composerFlags(options),
        become_password: becomePassword || undefined,
      });
      // Vers le détail : le flux d'events y est déjà branché, et c'est ce que
      // l'utilisateur veut voir. `replaceUrl` pour qu'un retour arrière ne
      // ramène pas sur un formulaire déjà soumis.
      await this.#router.navigate(['/runs', cree.run_ref], { replaceUrl: true });
    } catch (e: unknown) {
      // Les messages du serveur sont déjà écrits pour un lecteur humain — 403
      // nomme l'environnement refusé, 400 nomme le champ fautif, 409 dit
      // qu'un identifiant est déjà enregistré. Les reformuler mentirait sur ce
      // que le serveur a dit (même raisonnement que dans Login).
      const err = e as Partial<ApiError>;
      this.erreur.set(err?.message ?? 'launching the run failed');
    } finally {
      this.enCours.set(false);
    }
  }

  protected async annuler(): Promise<void> {
    await this.#router.navigate(['/runs']);
  }
}

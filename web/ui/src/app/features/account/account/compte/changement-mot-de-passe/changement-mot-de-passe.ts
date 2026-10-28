import { ChangeDetectionStrategy, Component, inject, signal } from '@angular/core';
import { Router } from '@angular/router';
import { AuthService } from '../../../../../core/services/auth/auth.service';
import { ApiError } from '../../../../../core/models/api-error';
import {
  ChangementMotDePasseForm,
  MotsDePasse,
} from './changement-mot-de-passe-form/changement-mot-de-passe-form';

/**
 * Changer son mot de passe.
 *
 * Le premier mot de passe d'un compte est GÉNÉRÉ par le serveur et montré une
 * seule fois : sans cet écran, quiconque se connecte avec n'a aucun moyen d'en
 * poser un à lui.
 *
 * Le succès DÉCONNECTE, parce que le serveur révoque toutes les sessions du
 * compte dans la même transaction. C'est annoncé avant, pas découvert après.
 */
@Component({
  selector: 'w-changement-mot-de-passe',
  imports: [ChangementMotDePasseForm],
  template: `<w-changement-mot-de-passe-form [enCours]="enCours()" [erreur]="erreur()" (soumis)="changer($event)" />`,
  host: { style: 'display: contents' },
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ChangementMotDePasse {
  readonly #auth = inject(AuthService);
  readonly #router = inject(Router);

  protected readonly enCours = signal(false);
  protected readonly erreur = signal<string | null>(null);

  protected async changer({ actuel, nouveau }: MotsDePasse): Promise<void> {
    if (this.enCours()) {
      return;
    }
    this.enCours.set(true);
    this.erreur.set(null);
    try {
      await this.#auth.changerMotDePasse(actuel, nouveau);
      await this.#router.navigate(['/login'], {
        queryParams: { raison: 'password-changed' },
      });
    } catch (e: unknown) {
      const err = e as Partial<ApiError>;
      // Le 400 porte la règle de robustesse, et c'est le SEUL message que le
      // serveur veut précis — l'appelant doit savoir ce qu'on attend de lui, et
      // cette information ne renseigne personne sur l'existence d'un compte. On
      // le rend donc tel quel plutôt que de le remplacer par un générique.
      this.erreur.set(err.message ?? 'the password could not be changed');
    } finally {
      this.enCours.set(false);
    }
  }
}

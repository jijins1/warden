import { ChangeDetectionStrategy, Component, input } from '@angular/core';
import { VaultEnvironnements } from './vault-environnements/vault-environnements';
import { VaultCle } from './vault-cle/vault-cle';
import { VaultChiffrement } from './vault-chiffrement/vault-chiffrement';

/**
 * Les clés Ansible Vault des environnements de l'appelant (ADR-0045).
 *
 * PAS derrière `adminGuard` : gérer la clé d'un environnement demande le même
 * droit qu'y lancer un run (ADR-0033), et le serveur le vérifie sur chaque
 * appel. Un administrateur sans droit sur l'environnement n'y peut rien.
 *
 * La page ne charge rien : elle lit l'environnement dans l'URL et le passe aux
 * trois blocs, qui chargent chacun leurs données.
 *
 * Pas de template de page du design system : c'est le seul écran « réglages
 * par environnement », il compose `w-page` et `w-page-head` directement. Le
 * template s'extraira au deuxième écran de ce type.
 */
@Component({
  selector: 'w-vault',
  imports: [VaultEnvironnements, VaultCle, VaultChiffrement],
  templateUrl: './vault.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class Vault {
  /** Lié depuis la route par `withComponentInputBinding` ; absent sur `/vault`. */
  readonly environment = input<string>();
}

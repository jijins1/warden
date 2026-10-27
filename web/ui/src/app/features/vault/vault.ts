import { ChangeDetectionStrategy, Component, input } from '@angular/core';
import { VaultEnvironnements } from './vault-environnements';
import { VaultCle } from './vault-cle';
import { VaultChiffrement } from './vault-chiffrement';

/**
 * Les clés Ansible Vault des environnements de l'appelant (ADR-0045).
 *
 * PAS derrière `adminGuard` : gérer la clé d'un environnement demande le même
 * droit qu'y lancer un run (ADR-0033), et le serveur le vérifie sur chaque
 * appel. Un administrateur sans droit sur l'environnement n'y peut rien.
 *
 * La page ne charge rien : elle lit l'environnement dans l'URL et le passe aux
 * trois blocs, qui chargent chacun leurs données.
 */
@Component({
  selector: 'w-vault',
  imports: [VaultEnvironnements, VaultCle, VaultChiffrement],
  templateUrl: './vault.html',
  styleUrl: './vault.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class Vault {
  /** Lié depuis la route par `withComponentInputBinding` ; absent sur `/vault`. */
  readonly environment = input<string>();
}

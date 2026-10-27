import { ChangeDetectionStrategy, Component, input, signal } from '@angular/core';
import { ClipboardModule } from '@angular/cdk/clipboard';
import { MatButtonModule } from '@angular/material/button';
import { MatIconModule } from '@angular/material/icon';

/** Un bloc `!vault |` affiché tel quel, avec de quoi le copier sans le retoucher. */
@Component({
  selector: 'w-bloc-vault-copiable',
  imports: [ClipboardModule, MatButtonModule, MatIconModule],
  templateUrl: './bloc-vault-copiable.html',
  styleUrl: './bloc-vault-copiable.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class BlocVaultCopiable {
  readonly bloc = input.required<string>();

  protected readonly copie = signal(false);
}

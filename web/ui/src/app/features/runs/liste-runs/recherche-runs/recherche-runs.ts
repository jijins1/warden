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
import { Router, RouterLink } from '@angular/router';
import { FiltreRuns, RunsService } from '../../../../core/services/runs/runs.service';
import { ApiError } from '../../../../core/models/api-error';
import { PAGE_RECHERCHE } from '../../../../design-system/page-recherche/page-recherche';
import { Pagination } from '../../../../design-system/pagination/pagination';
import { FiltresRuns } from './filtres-runs/filtres-runs';
import { FiltreEnvironnement } from '../../../filtre-environnement/filtre-environnement';
import { FiltresActifs } from './filtres-actifs/filtres-actifs';
import { EtatResultats, ResultatsRuns } from './resultats-runs/resultats-runs';

/**
 * Un changement de filtres : `null` retire le filtre de l'URL.
 *
 * Les clés sont celles de `FiltreRuns`, qui sont aussi les noms des paramètres
 * de requête — l'URL et l'API parlent le même vocabulaire.
 */
export type PatchFiltre = { [K in keyof FiltreRuns]?: FiltreRuns[K] | null };

/** Tous les filtres que l'URL peut porter, pour « Clear filters ». */
const CLES_FILTRE: readonly (keyof FiltreRuns)[] = [
  'env',
  'inventory',
  'state',
  'since',
  'until',
  'changed_min',
  'mine',
];

/**
 * Les runs que l'appelant peut voir, sous les filtres reçus.
 *
 * Les siens, et TOUS ceux des environnements sur lesquels il a un droit vivant,
 * quel qu'en soit l'auteur — la portée est posée par le serveur
 * (`VisibleRunFilter`), qui écrase le propriétaire au lieu de le valider : aucun
 * paramètre ne l'élargit, et être administrateur non plus. Ce n'est pas une vue
 * de parc.
 *
 * Il reçoit les filtres de la page, qui les lit dans l'URL, et les ÉCRIT dans
 * l'URL quand on les change : la page les relit, et la boucle se referme.
 */
@Component({
  selector: 'w-recherche-runs',
  imports: [
    RouterLink,
    PAGE_RECHERCHE,
    Pagination,
    FiltresRuns,
    FiltreEnvironnement,
    FiltresActifs,
    ResultatsRuns,
  ],
  templateUrl: './recherche-runs.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class RechercheRuns {
  readonly #runs = inject(RunsService);
  readonly #router = inject(Router);

  readonly filtre = input.required<FiltreRuns>();

  protected readonly etat = signal<EtatResultats>({
    runs: [],
    chargement: false,
    charge: false,
    erreur: null,
  });
  readonly #curseur = signal<string | null>(null);

  protected readonly reste = computed(() => this.#curseur() !== null);
  protected readonly chargement = computed(() => this.etat().chargement);
  protected readonly nombreAffiches = computed(() => this.etat().runs.length);
  protected readonly avecResultats = computed(() => this.nombreAffiches() > 0);
  protected readonly environnementActif = computed(() => this.filtre().env ?? null);
  protected readonly filtrePose = computed(() =>
    Object.values(this.filtre()).some((valeur) => valeur !== undefined),
  );

  constructor() {
    // Un `effect` sur les filtres, et non un chargement au constructeur : les
    // filtres vivent dans l'URL, donc les boutons précédent/suivant du
    // navigateur les changent sans que le composant soit reconstruit. Recharger
    // à l'initialisation seule afficherait les résultats de l'URL précédente
    // sous les filtres de la nouvelle — le pire des deux, puisque rien ne
    // signalerait l'écart.
    effect(() => {
      this.filtre();
      untracked(() => void this.recharger());
    });
  }

  /** Première page : remplace tout. */
  protected async recharger(): Promise<void> {
    this.etat.update((e) => ({ ...e, runs: [] }));
    this.#curseur.set(null);
    await this.#charger(undefined);
  }

  /** Page suivante : ajoute à la suite. Jamais un numéro de page. */
  protected async suivante(): Promise<void> {
    const curseur = this.#curseur();
    if (curseur) {
      await this.#charger(curseur);
    }
  }

  protected async appliquer(patch: PatchFiltre): Promise<void> {
    // Naviguer SUFFIT : l'effet ci-dessus observe les filtres et recharge.
    // Appeler `recharger()` ici en plus déclencherait deux requêtes pour un
    // seul clic. `[]` sans `relativeTo` garde l'URL courante et ne change que
    // ses paramètres : le container n'a pas à savoir sous quelle route il vit.
    await this.#router.navigate([], { queryParams: patch, queryParamsHandling: 'merge' });
  }

  protected async effacer(): Promise<void> {
    await this.appliquer(Object.fromEntries(CLES_FILTRE.map((cle) => [cle, null])));
  }

  /** Sans curseur, la page reçue remplace la liste ; avec, elle s'y ajoute. */
  async #charger(curseur: string | undefined): Promise<void> {
    this.etat.update((e) => ({ ...e, chargement: true, erreur: null }));
    try {
      const page = await this.#runs.lister(this.filtre(), curseur);
      const recus = page.Runs ?? [];
      this.etat.update((e) => ({ ...e, runs: curseur ? [...e.runs, ...recus] : recus }));
      this.#curseur.set(page.NextCursor || null);
    } catch (e: unknown) {
      const err = e as Partial<ApiError>;
      this.etat.update((etat) => ({ ...etat, erreur: err?.message ?? 'listing runs failed' }));
    } finally {
      this.etat.update((e) => ({ ...e, chargement: false, charge: true }));
    }
  }
}

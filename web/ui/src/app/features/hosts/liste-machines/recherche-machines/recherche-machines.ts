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
import { Router } from '@angular/router';
import { FiltreMachines, MachinesService } from '../../../../core/services/machines/machines.service';
import { MachineEnrolee } from '../../../../core/models/wire';
import { ApiError } from '../../../../core/models/api-error';
import { PAGE_RECHERCHE } from '../../../../design-system/page-recherche/page-recherche';
import { Pagination } from '../../../../design-system/pagination/pagination';
import { ChampRecherche } from '../../../../design-system/champ-recherche/champ-recherche';
import { FiltreEnvironnement } from '../../../filtre-environnement/filtre-environnement';
import { FiltresActifsMachines } from './filtres-actifs-machines/filtres-actifs-machines';
import { EtatResultatsMachines, ResultatsMachines } from './resultats-machines/resultats-machines';
import { LigneMachine } from './resultats-machines/table-machines/table-machines';

/**
 * Un changement de filtres : `null` retire le filtre de l'URL. Les clés sont
 * les noms des paramètres de `GET /hosts`, donc aussi ceux de l'URL.
 */
export type PatchFiltreMachines = { [K in keyof FiltreMachines]?: FiltreMachines[K] | null };

/** Tous les filtres que l'URL peut porter, pour « Clear filters ». */
const CLES_FILTRE: readonly (keyof FiltreMachines)[] = ['env', 'q'];

/** Le port SSH par défaut : le serveur l'omet, et l'afficher n'apprendrait rien. */
const PORT_SSH = 22;

/**
 * Les machines enrôlées que l'appelant peut voir, sous les filtres reçus.
 *
 * Celles des environnements où il a un droit vivant, et seulement celles-là :
 * la portée est posée par le serveur, et être administrateur ne l'élargit pas.
 *
 * Il reçoit les filtres de la page, qui les lit dans l'URL, et les ÉCRIT dans
 * l'URL quand on les change : la page les relit, et la boucle se referme.
 */
@Component({
  selector: 'w-recherche-machines',
  imports: [
    PAGE_RECHERCHE,
    Pagination,
    ChampRecherche,
    FiltreEnvironnement,
    FiltresActifsMachines,
    ResultatsMachines,
  ],
  templateUrl: './recherche-machines.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class RechercheMachines {
  readonly #machines = inject(MachinesService);
  readonly #router = inject(Router);

  readonly filtre = input.required<FiltreMachines>();

  protected readonly etat = signal<EtatResultatsMachines>({
    lignes: [],
    chargement: false,
    charge: false,
    erreur: null,
  });
  readonly #curseur = signal<string | null>(null);

  protected readonly reste = computed(() => this.#curseur() !== null);
  protected readonly chargement = computed(() => this.etat().chargement);
  protected readonly nombreAffiches = computed(() => this.etat().lignes.length);
  protected readonly avecResultats = computed(() => this.nombreAffiches() > 0);
  protected readonly environnementActif = computed(() => this.filtre().env ?? null);
  protected readonly recherche = computed(() => this.filtre().q ?? null);
  protected readonly filtrePose = computed(() =>
    Object.values(this.filtre()).some((valeur) => valeur !== undefined),
  );

  constructor() {
    // Un `effect` sur les filtres, et non un chargement au constructeur : les
    // boutons précédent/suivant du navigateur changent l'URL sans reconstruire
    // le composant (voir `RechercheRuns`).
    effect(() => {
      this.filtre();
      untracked(() => void this.recharger());
    });
  }

  /** Première page : remplace tout. */
  protected async recharger(): Promise<void> {
    this.etat.update((e) => ({ ...e, lignes: [] }));
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

  protected async appliquer(patch: PatchFiltreMachines): Promise<void> {
    // Naviguer SUFFIT : l'effet ci-dessus observe les filtres et recharge.
    await this.#router.navigate([], { queryParams: patch, queryParamsHandling: 'merge' });
  }

  protected async effacer(): Promise<void> {
    await this.appliquer(Object.fromEntries(CLES_FILTRE.map((cle) => [cle, null])));
  }

  /** Sans curseur, la page reçue remplace la liste ; avec, elle s'y ajoute. */
  async #charger(curseur: string | undefined): Promise<void> {
    this.etat.update((e) => ({ ...e, chargement: true, erreur: null }));
    try {
      const page = await this.#machines.lister(this.filtre(), curseur);
      const recues = (page.hosts ?? []).map(versLigne);
      this.etat.update((e) => ({ ...e, lignes: curseur ? [...e.lignes, ...recues] : recues }));
      this.#curseur.set(page.next_cursor || null);
    } catch (e: unknown) {
      const err = e as Partial<ApiError>;
      this.etat.update((etat) => ({ ...etat, erreur: err?.message ?? 'listing machines failed' }));
    } finally {
      this.etat.update((e) => ({ ...e, chargement: false, charge: true }));
    }
  }
}

function versLigne(m: MachineEnrolee): LigneMachine {
  const port = m.port ?? PORT_SSH;
  return {
    cle: `${m.environment}/${m.host}`,
    host: m.host,
    environnement: m.environment,
    adresse: port === PORT_SSH ? m.address : `${m.address}:${port}`,
    login: m.login,
    // « Verified » : warden s'est connecté lui-même pour poser la clé. Un mode
    // d'enrôlement, pas un état : il se dit en texte, pas en couleur.
    enrolement: m.mode === 'assisted' ? 'Verified' : 'Declared',
    enroleeLe: m.enrolled_at,
  };
}

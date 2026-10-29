/** Ce qui est arrivé au fichier entre les deux arbres. */
export type StatutFichier = 'added' | 'deleted' | 'modified' | 'renamed';

/** Une ligne d'un bloc, numérotée dans l'arbre de gauche, celui de droite, ou les deux. */
export interface LigneBloc {
  readonly genre: 'add' | 'del' | 'ctx' | 'note';
  readonly texte: string;
  readonly ancien: number | null;
  readonly nouveau: number | null;
}

/** Un bloc `@@` : seule la section qui suit l'en-tête est gardée, les plages se lisent dans les numéros. */
export interface BlocDiff {
  readonly section: string;
  readonly lignes: readonly LigneBloc[];
}

export interface FichierDiff {
  /** Le chemin dans l'arbre de droite — celui de gauche pour un fichier supprimé. */
  readonly chemin: string;
  /** Le chemin d'origine, posé seulement pour un renommage. */
  readonly ancienChemin: string | null;
  readonly statut: StatutFichier;
  readonly binaire: boolean;
  readonly ajouts: number;
  readonly retraits: number;
  readonly blocs: readonly BlocDiff[];
}

const EN_TETE_BLOC = /^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@ ?(.*)$/;

/**
 * Découpe la sortie de `git diff` en fichiers.
 *
 * Les lignes propres à git (`diff --git`, `index`, `---`/`+++`, modes) ne
 * servent qu'à poser le statut et les chemins : elles ne sont pas restituées.
 *
 * Un bloc se lit d'après les longueurs de son en-tête, pas d'après le premier
 * caractère de chaque ligne : une ligne de contexte vide, dont un outil a
 * mangé l'espace, reste du contexte, et un `---` dans le corps reste un retrait.
 *
 * Un texte qui ne commence pas par `diff --git` ne donne aucun fichier : à
 * l'appelant de l'afficher tel quel.
 */
export function parserDiff(texte: string): FichierDiff[] {
  const fichiers: FichierDiff[] = [];
  const lignes = texte.split('\n');
  let i = 0;

  while (i < lignes.length) {
    if (!lignes[i].startsWith('diff --git ')) {
      i++;
      continue;
    }
    const [gauche, droite] = cheminsDeLEnTete(lignes[i]);
    let ancien: string | null = gauche;
    let nouveau: string | null = droite;
    let statut: StatutFichier = 'modified';
    let binaire = false;
    i++;

    // Les métadonnées, jusqu'au premier bloc ou au fichier suivant.
    while (i < lignes.length && !lignes[i].startsWith('diff --git ') && !lignes[i].startsWith('@@')) {
      const l = lignes[i];
      if (l.startsWith('new file mode')) statut = 'added';
      else if (l.startsWith('deleted file mode')) statut = 'deleted';
      else if (l.startsWith('rename from ')) { statut = 'renamed'; ancien = l.slice(12); }
      else if (l.startsWith('rename to ')) { statut = 'renamed'; nouveau = l.slice(10); }
      else if (l.startsWith('--- ')) ancien = cheminDeLaCible(l.slice(4)) ?? ancien;
      else if (l.startsWith('+++ ')) nouveau = cheminDeLaCible(l.slice(4)) ?? nouveau;
      else if (l.startsWith('Binary files ') || l === 'GIT binary patch') binaire = true;
      i++;
    }

    const blocs: BlocDiff[] = [];
    let ajouts = 0;
    let retraits = 0;
    while (i < lignes.length && lignes[i].startsWith('@@')) {
      const m = EN_TETE_BLOC.exec(lignes[i]);
      i++;
      if (!m) continue;
      let numAncien = Number(m[1]);
      let numNouveau = Number(m[3]);
      let resteAncien = m[2] === undefined ? 1 : Number(m[2]);
      let resteNouveau = m[4] === undefined ? 1 : Number(m[4]);
      const contenu: LigneBloc[] = [];

      while (i < lignes.length && (resteAncien > 0 || resteNouveau > 0 || lignes[i].startsWith('\\'))) {
        const l = lignes[i];
        // Une ligne de contexte commence par une espace : un en-tête de bloc ou
        // de fichier clôt donc le bloc, même si ses longueurs annonçaient plus.
        if (l.startsWith('@@') || l.startsWith('diff --git ')) break;
        const marque = l[0];
        if (marque === '\\') {
          contenu.push({ genre: 'note', texte: l.slice(2), ancien: null, nouveau: null });
        } else if (marque === '+') {
          contenu.push({ genre: 'add', texte: l.slice(1), ancien: null, nouveau: numNouveau++ });
          resteNouveau--;
          ajouts++;
        } else if (marque === '-') {
          contenu.push({ genre: 'del', texte: l.slice(1), ancien: numAncien++, nouveau: null });
          resteAncien--;
          retraits++;
        } else {
          contenu.push({ genre: 'ctx', texte: l.slice(1), ancien: numAncien++, nouveau: numNouveau++ });
          resteAncien--;
          resteNouveau--;
        }
        i++;
      }
      blocs.push({ section: m[5], lignes: contenu });
    }

    fichiers.push({
      chemin: (statut === 'deleted' ? ancien : nouveau) ?? ancien ?? '',
      ancienChemin: statut === 'renamed' ? ancien : null,
      statut,
      binaire,
      ajouts,
      retraits,
      blocs,
    });
  }
  return fichiers;
}

/** `a/x b/y` : coupé sur ` b/`, ce qui suffit tant que le chemin ne contient pas lui-même ` b/`. */
function cheminsDeLEnTete(ligne: string): [string | null, string | null] {
  const reste = ligne.slice('diff --git '.length);
  const coupure = reste.indexOf(' b/');
  if (!reste.startsWith('a/') || coupure < 0) return [null, null];
  return [reste.slice(2, coupure), reste.slice(coupure + 3)];
}

/** `a/x`, `b/x`, `"a/x y"` ou `/dev/null` — ce dernier ne nomme aucun fichier. */
function cheminDeLaCible(cible: string): string | null {
  const nette = cible.replace(/\t.*$/, '').replace(/^"(.*)"$/, '$1');
  if (nette === '/dev/null') return null;
  return nette.replace(/^[ab]\//, '');
}

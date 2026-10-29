import Prism from 'prismjs';
import 'prismjs/components/prism-yaml';
import 'prismjs/components/prism-markup-templating';
import 'prismjs/components/prism-django';
import 'prismjs/components/prism-bash';
import 'prismjs/components/prism-python';
import 'prismjs/components/prism-ini';
import 'prismjs/components/prism-json';
import 'prismjs/components/prism-nginx';

/** Un morceau de ligne et la classe qui le peint ; `null` pour le texte sans rôle. */
export interface Fragment {
  readonly texte: string;
  readonly genre: Genre | null;
}

type Genre = 'cle' | 'chaine' | 'valeur' | 'commentaire' | 'gabarit' | 'mot' | 'ponct';

/**
 * Les types de Prism, ramenés à sept rôles. Un type absent d'ici hérite du rôle
 * de son parent : l'intérieur d'un `{{ … }}` reste du gabarit.
 */
const GENRES: Record<string, Genre> = {
  key: 'cle', atrule: 'cle', property: 'cle', 'attr-name': 'cle', selector: 'cle', section: 'cle',
  string: 'chaine', char: 'chaine', 'attr-value': 'chaine', scalar: 'chaine', regex: 'chaine',
  number: 'valeur', boolean: 'valeur', constant: 'valeur', null: 'valeur', important: 'valeur',
  comment: 'commentaire', prolog: 'commentaire', shebang: 'commentaire',
  jinja: 'gabarit', delimiter: 'gabarit', variable: 'gabarit',
  keyword: 'mot', builtin: 'mot', function: 'mot', directive: 'mot',
  punctuation: 'ponct', operator: 'ponct',
};

/** `{{ … }}`, `{% … %}`, `{# … #}` : ce qu'Ansible évalue, partout où il peut apparaître. */
const JINJA: Prism.GrammarValue = {
  pattern: /\{\{[\s\S]*?\}\}|\{%[\s\S]*?%\}|\{#[\s\S]*?#\}/,
  inside: Prism.languages['django'],
};

/** Le Jinja passe AVANT le reste de la grammaire, et à l'intérieur des chaînes YAML. */
function avecJinja(nom: string): Prism.Grammar {
  const grammaire = Prism.languages.extend(nom, {});
  if (nom === 'yaml') {
    (grammaire['string'] as Prism.GrammarRest & { inside?: Prism.Grammar }).inside = { jinja: JINJA };
  }
  return { jinja: JINJA, ...grammaire };
}

const ANSIBLE = avecJinja('yaml');
const SEUL_JINJA: Prism.Grammar = { jinja: JINJA };

/**
 * La grammaire d'un fichier d'après son chemin. Un `.j2` est un gabarit : on
 * colore le Jinja, et le langage du fichier rendu quand l'extension le dit
 * (`nginx.conf.j2`, `vars.yml.j2`).
 */
export function grammairePour(chemin: string): Prism.Grammar | null {
  const gabarit = /\.(j2|jinja2?)$/.test(chemin);
  const rendu = chemin.replace(/\.(j2|jinja2?)$/, '');
  const nom = langage(rendu);
  if (nom === 'yaml') return ANSIBLE;
  if (gabarit) return nom ? avecJinja(nom) : SEUL_JINJA;
  return nom ? Prism.languages[nom] : null;
}

function langage(chemin: string): string | null {
  const fichier = chemin.split('/').pop() ?? '';
  const extension = fichier.includes('.') ? fichier.split('.').pop()!.toLowerCase() : '';
  switch (extension) {
    case 'yml':
    case 'yaml':
      return 'yaml';
    case 'sh':
    case 'bash':
      return 'bash';
    case 'py':
      return 'python';
    case 'json':
      return 'json';
    case 'ini':
    case 'cfg':
      return 'ini';
    case 'conf':
      return /nginx/.test(chemin) ? 'nginx' : 'ini';
    case '':
      // Les fichiers de variables d'Ansible n'ont souvent pas d'extension.
      return /(^|\/)(group_vars|host_vars|vars|defaults)\//.test(chemin) ? 'yaml' : null;
    default:
      return null;
  }
}

/**
 * Découpe une ligne en fragments colorés.
 *
 * La ligne est tokenisée seule : une chaîne multi-ligne perd sa couleur au-delà
 * de sa première ligne. C'est le prix d'un diff, qui ne montre que des morceaux
 * de fichier.
 *
 * Prism ne produit ici que des jetons, jamais de HTML : le texte est rendu par
 * interpolation, comme le reste du diff.
 */
export function colorer(texte: string, grammaire: Prism.Grammar | null): Fragment[] {
  if (!grammaire || !texte) return [{ texte, genre: null }];
  const fragments: Fragment[] = [];
  const aplatir = (jeton: string | Prism.Token | (string | Prism.Token)[], herite: Genre | null) => {
    if (typeof jeton === 'string') {
      fragments.push({ texte: jeton, genre: herite });
    } else if (Array.isArray(jeton)) {
      jeton.forEach((j) => aplatir(j, herite));
    } else {
      const alias = Array.isArray(jeton.alias) ? jeton.alias[0] : jeton.alias;
      aplatir(jeton.content, GENRES[jeton.type] ?? (alias ? GENRES[alias] : undefined) ?? herite);
    }
  };
  aplatir(Prism.tokenize(texte, grammaire), null);
  return fragments;
}

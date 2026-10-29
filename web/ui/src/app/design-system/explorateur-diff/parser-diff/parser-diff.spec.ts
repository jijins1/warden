import { describe, expect, it } from 'vitest';
import { parserDiff } from './parser-diff';

const diff = (...lignes: string[]) => lignes.join('\n') + '\n';

describe('parserDiff', () => {
  it('numérote les lignes des deux côtés et compte ajouts et retraits', () => {
    const [f] = parserDiff(
      diff(
        'diff --git a/site.yml b/site.yml',
        'index 1111111..2222222 100644',
        '--- a/site.yml',
        '+++ b/site.yml',
        '@@ -10,3 +10,3 @@ - hosts: web',
        ' garde',
        '-retire',
        '+ajoute',
        ' garde',
      ),
    );

    expect(f).toMatchObject({ chemin: 'site.yml', statut: 'modified', ajouts: 1, retraits: 1 });
    expect(f.blocs[0].section).toBe('- hosts: web');
    expect(f.blocs[0].lignes).toEqual([
      { genre: 'ctx', texte: 'garde', ancien: 10, nouveau: 10 },
      { genre: 'del', texte: 'retire', ancien: 11, nouveau: null },
      { genre: 'add', texte: 'ajoute', ancien: null, nouveau: 11 },
      { genre: 'ctx', texte: 'garde', ancien: 12, nouveau: 12 },
    ]);
  });

  // Les longueurs de l'en-tête bornent le bloc : un `---` dans le corps est un
  // retrait (ici un séparateur YAML retiré), pas l'en-tête d'un autre fichier.
  it('lit un `---` du corps comme un retrait', () => {
    const [f] = parserDiff(
      diff('diff --git a/a.yml b/a.yml', '--- a/a.yml', '+++ b/a.yml', '@@ -1,2 +1 @@', '---', ' x'),
    );
    expect(f.retraits).toBe(1);
    expect(f.blocs[0].lignes[0]).toMatchObject({ genre: 'del', texte: '--' });
  });

  it('clôt un bloc sur l’en-tête suivant, même si ses longueurs annonçaient plus', () => {
    const [f] = parserDiff(
      diff('diff --git a/a.yml b/a.yml', '--- a/a.yml', '+++ b/a.yml', '@@ -1,3 +1,3 @@', ' x', '@@ -9 +9 @@ fin', '-y', '+z'),
    );
    expect(f.blocs.map((b) => b.section)).toEqual(['', 'fin']);
    expect(f.blocs[1].lignes).toHaveLength(2);
  });

  it('pose le statut des créations, suppressions et renommages', () => {
    const fichiers = parserDiff(
      diff(
        'diff --git a/neuf.yml b/neuf.yml',
        'new file mode 100644',
        '--- /dev/null',
        '+++ b/neuf.yml',
        '@@ -0,0 +1 @@',
        '+x',
        'diff --git a/vieux.yml b/vieux.yml',
        'deleted file mode 100644',
        '--- a/vieux.yml',
        '+++ /dev/null',
        '@@ -1 +0,0 @@',
        '-x',
        'diff --git a/avant.yml b/apres.yml',
        'similarity index 100%',
        'rename from avant.yml',
        'rename to apres.yml',
      ),
    );

    expect(fichiers.map((f) => [f.chemin, f.statut, f.ancienChemin])).toEqual([
      ['neuf.yml', 'added', null],
      ['vieux.yml', 'deleted', null],
      ['apres.yml', 'renamed', 'avant.yml'],
    ]);
  });

  it('signale un binaire sans bloc', () => {
    const [f] = parserDiff(diff('diff --git a/logo.png b/logo.png', 'Binary files a/logo.png and b/logo.png differ'));
    expect(f.binaire).toBe(true);
    expect(f.blocs).toHaveLength(0);
  });

  it('ne reconnaît rien dans un texte qui n’est pas un diff git', () => {
    expect(parserDiff('--- before\n+++ after\n@@ -1 +1 @@\n-a\n+b\n')).toEqual([]);
  });
});

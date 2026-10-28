// @ts-check
const eslint = require('@eslint/js');
const { defineConfig } = require('eslint/config');
const tseslint = require('typescript-eslint');
const angular = require('angular-eslint');

module.exports = defineConfig([
  {
    files: ['**/*.ts'],
    extends: [
      eslint.configs.recommended,
      tseslint.configs.recommended,
      tseslint.configs.stylistic,
      angular.configs.tsRecommended,
    ],
    processor: angular.processInlineTemplates,
    rules: {
      '@angular-eslint/directive-selector': [
        'error',
        {
          type: 'attribute',
          prefix: 'w',
          style: 'camelCase',
        },
      ],
      '@angular-eslint/component-selector': [
        'error',
        {
          type: 'element',
          prefix: 'w',
          style: 'kebab-case',
        },
      ],
    },
  },
  {
    // Les templates du design system exposent des EMPLACEMENTS nommés
    // (`<w-page-recherche-titre>`) : une directive par emplacement, dont le
    // sélecteur est un élément. Un attribut ferait projeter le contenu par une
    // classe ou un attribut posé à la main, et le nom de l'emplacement ne se
    // lirait plus dans le template de l'écran.
    files: ['src/app/design-system/page-*/*.ts', 'src/app/design-system/masthead/*.ts'],
    rules: {
      '@angular-eslint/directive-selector': [
        'error',
        { type: 'element', prefix: 'w', style: 'kebab-case' },
      ],
    },
  },
  {
    files: ['**/*.html'],
    extends: [angular.configs.templateRecommended, angular.configs.templateAccessibility],
    rules: {},
  },
]);

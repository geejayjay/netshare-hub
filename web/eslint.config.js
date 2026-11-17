import boundaries from 'eslint-plugin-boundaries'
import reactHooks from 'eslint-plugin-react-hooks'
import tseslint from 'typescript-eslint'

/**
 * The architecture rules are enforced here, not just written in a document.
 *
 *   app      -> may use everything      (app + routes are the composition layer:
 *   routes   -> app, layouts, features, shared   the only code that knows every part)
 *   layouts  -> shared only            (a layout never knows about a feature)
 *   features -> shared, and other features only through their index.ts
 *   shared   -> shared only            (shared code never knows about a feature)
 */
export default tseslint.config(
  { ignores: ['dist', 'src/routeTree.gen.ts', 'src/shared/api/schema.d.ts'] },
  ...tseslint.configs.recommended,
  {
    files: ['src/**/*.{ts,tsx}'],
    plugins: { boundaries, 'react-hooks': reactHooks },
    settings: {
      'import/resolver': { typescript: { project: './tsconfig.json' } },
      'boundaries/include': ['src/**/*'],
      'boundaries/elements': [
        { type: 'app', pattern: 'src/app/**', mode: 'full' },
        { type: 'routes', pattern: 'src/routes/**', mode: 'full' },
        { type: 'layouts', pattern: 'src/layouts/**', mode: 'full' },
        { type: 'feature', pattern: 'src/features/*', mode: 'folder', capture: ['name'] },
        { type: 'shared', pattern: 'src/shared/**', mode: 'full' },
      ],
    },
    rules: {
      ...reactHooks.configs.recommended.rules,
      'boundaries/element-types': [
        'error',
        {
          default: 'disallow',
          rules: [
            { from: 'app', allow: ['app', 'routes', 'layouts', 'feature', 'shared'] },
            { from: 'routes', allow: ['app', 'routes', 'layouts', 'feature', 'shared'] },
            { from: 'layouts', allow: ['layouts', 'shared'] },
            { from: 'feature', allow: ['shared', 'feature'] },
            { from: 'shared', allow: ['shared'] },
          ],
        },
      ],
      // A feature is used through its index.ts only; its inner files are private.
      'boundaries/entry-point': [
        'error',
        {
          default: 'disallow',
          rules: [
            { target: ['feature'], allow: 'index.ts' },
            { target: ['app', 'routes', 'layouts', 'shared'], allow: '*' },
          ],
        },
      ],
      // Informational only: TanStack Table opts out of the React Compiler.
      'react-hooks/incompatible-library': 'off',
      '@typescript-eslint/no-unused-vars': ['error', { argsIgnorePattern: '^_', varsIgnorePattern: '^_' }],
    },
  },
)

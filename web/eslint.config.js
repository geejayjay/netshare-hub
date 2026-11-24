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
        { type: 'app', pattern: 'src/app/**', partialMatch: false },
        { type: 'routes', pattern: 'src/routes/**', partialMatch: false },
        { type: 'layouts', pattern: 'src/layouts/**', partialMatch: false },
        { type: 'feature', pattern: 'src/features/*', capture: ['name'] },
        { type: 'shared', pattern: 'src/shared/**', partialMatch: false },
      ],
    },
    rules: {
      ...reactHooks.configs.recommended.rules,
      'boundaries/dependencies': [
        'error',
        {
          default: 'disallow',
          policies: [
            {
              from: { element: { type: 'app' } },
              allow: [
                { to: { element: { type: 'app' } } },
                { to: { element: { type: 'routes' } } },
                { to: { element: { type: 'layouts' } } },
                { to: { element: { type: 'feature' } } },
                { to: { element: { type: 'shared' } } },
              ],
            },
            {
              from: { element: { type: 'routes' } },
              allow: [
                { to: { element: { type: 'app' } } },
                { to: { element: { type: 'routes' } } },
                { to: { element: { type: 'layouts' } } },
                { to: { element: { type: 'feature' } } },
                { to: { element: { type: 'shared' } } },
              ],
            },
            {
              from: { element: { type: 'layouts' } },
              allow: [{ to: { element: { type: 'layouts' } } }, { to: { element: { type: 'shared' } } }],
            },
            {
              from: { element: { type: 'feature' } },
              allow: [
                { to: { element: { type: 'shared' } } },
                { to: { element: { type: 'feature' }, internalPath: 'index.ts' } },
                { to: { element: { type: 'feature', captured: { name: '{{ from.captured.name }}' } } } },
              ],
            },
            {
              from: { element: { type: 'shared' } },
              allow: [{ to: { element: { type: 'shared' } } }],
            },
          ],
        },
      ],
      // Informational only: TanStack Table opts out of the React Compiler.
      'react-hooks/incompatible-library': 'off',
      '@typescript-eslint/no-unused-vars': ['error', { argsIgnorePattern: '^_', varsIgnorePattern: '^_' }],
    },
  },
)

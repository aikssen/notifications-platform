// @ts-check
import js from '@eslint/js';
import tseslint from 'typescript-eslint';
import importPlugin from 'eslint-plugin-import';

/**
 * Services that follow the full hexagonal layout. `demo-tools` is deliberately
 * excluded: it is a set of demo scripts, not a product service (ADR 0003).
 */
const HEXAGONAL_SERVICES = [
  'self-service-api',
  'subscription-service',
  'monitor-service',
];

/**
 * Modules that reach the outside world. The domain and the application layer
 * are not allowed to know they exist — that is what makes the use cases
 * testable without Kafka, PostgreSQL or an HTTP server.
 */
const IO_MODULES = [
  'pg',
  'kafkajs',
  'express',
  'axios',
  'jsonwebtoken',
  'helmet',
  'pino',
  'prom-client',
  'node:fs',
  'node:fs/promises',
  'node:http',
  'node:https',
  'node:net',
  'node:dns',
  'node:dns/promises',
  'node:child_process',
];

/**
 * THE DEPENDENCY RULE, as a build failure.
 *
 *   domain         imports nothing
 *   application    imports only domain
 *   infrastructure imports both
 *
 * Enforced in two complementary ways: `import/no-restricted-paths` blocks
 * inward-pointing imports between layers, and `no-restricted-imports` blocks
 * infrastructure libraries from being pulled into the core.
 */
const layerZones = HEXAGONAL_SERVICES.flatMap((service) => {
  const src = `./services/${service}/src`;
  return [
    {
      target: `${src}/domain`,
      from: `${src}/application`,
      message: 'domain must not import application — the dependency rule points inward.',
    },
    {
      target: `${src}/domain`,
      from: `${src}/infrastructure`,
      message: 'domain must not import infrastructure — the dependency rule points inward.',
    },
    {
      target: `${src}/application`,
      from: `${src}/infrastructure`,
      message:
        'application must not import infrastructure — depend on a port, and wire the adapter in main.ts.',
    },
  ];
});

export default tseslint.config(
  {
    ignores: ['**/dist/**', '**/node_modules/**', '**/coverage/**', '**/*.js'],
  },

  js.configs.recommended,
  ...tseslint.configs.recommended,

  {
    files: ['services/**/*.ts'],
    plugins: { import: importPlugin },
    languageOptions: {
      ecmaVersion: 2023,
      sourceType: 'module',
    },
    settings: {
      'import/resolver': {
        typescript: { project: 'services/*/tsconfig.json' },
      },
    },
    rules: {
      'import/no-restricted-paths': ['error', { zones: layerZones }],
      '@typescript-eslint/no-explicit-any': 'error',
      '@typescript-eslint/consistent-type-imports': 'warn',
      '@typescript-eslint/no-unused-vars': [
        'error',
        { argsIgnorePattern: '^_', varsIgnorePattern: '^_' },
      ],
      eqeqeq: ['error', 'always'],
      'no-console': 'error',
    },
  },

  // The core of every hexagonal service: no I/O libraries, ever.
  {
    files: HEXAGONAL_SERVICES.flatMap((s) => [
      `services/${s}/src/domain/**/*.ts`,
      `services/${s}/src/application/**/*.ts`,
    ]),
    rules: {
      'no-restricted-imports': [
        'error',
        {
          paths: IO_MODULES.map((name) => ({
            name,
            message: `${name} is infrastructure. Define a port in application/ports.ts and implement the adapter under infrastructure/.`,
          })),
          patterns: [
            {
              group: ['**/infrastructure/**', '@/infrastructure/**'],
              message:
                'The core must not reach into infrastructure. Depend on a port instead.',
            },
          ],
        },
      ],
    },
  },

  // Demo scripts and tests are allowed to be pragmatic.
  {
    files: ['services/demo-tools/**/*.ts', 'services/**/*.test.ts', 'scripts/**/*.ts'],
    rules: {
      'no-console': 'off',
      '@typescript-eslint/no-explicit-any': 'off',
    },
  },
);

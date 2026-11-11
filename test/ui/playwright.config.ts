import { defineConfig } from '@playwright/test';

// The console is reached inside the Compose network; see docs/testing.md.
export default defineConfig({
  testDir: '.',
  testMatch: /.*\.spec\.ts/,
  timeout: 10 * 60_000,
  expect: { timeout: 15_000 },
  workers: 1,
  fullyParallel: false,
  retries: 0,
  reporter: [['list'], ['json', { outputFile: 'test-results/results.json' }], ['html', { open: 'never' }]],
  use: {
    baseURL: process.env.CONSOLE_URL ?? 'http://console.agentbox.corp:18080',
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
});

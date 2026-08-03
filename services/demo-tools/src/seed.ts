/**
 * CLI wrapper. Also available as POST /simulate/seed.
 */
import { env } from './fixture.js';
import { seed } from './simulate.js';

const result = await seed(
  env('DATABASE_URL'),
  process.env.FIXTURE_WEBHOOK_URL ?? 'https://client.example.com/webhooks',
);

console.log(`seeded ${result.seeded} events from the case statement fixture`);
for (const [state, count] of Object.entries(result.byState)) {
  console.log(`  ${state.padEnd(10)} ${count}`);
}
console.log('\nthe FAILED events are the ones the replay endpoint acts on');

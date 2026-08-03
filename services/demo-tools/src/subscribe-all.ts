/**
 * CLI wrapper. Also available as POST /simulate/subscribe-all, which accepts a
 * webhook_url in the body — use that on presentation day with the public HTTPS
 * endpoint the panel provides.
 */
import { env } from './fixture.js';
import { subscribeAll } from './simulate.js';

const webhookUrl = env('WEBHOOK_URL', 'http://localhost:3004/webhook');

const result = await subscribeAll({
  subscriptionsBaseUrl: env('SUBSCRIPTIONS_BASE_URL', 'http://localhost:3001'),
  webhookUrl,
  expectedStatus: Number(process.env.EXPECTED_STATUS ?? 200),
  receiverUrl: process.env.WEBHOOK_CONTROL_URL ?? 'http://localhost:3004',
});

console.log(`registering subscriptions against ${webhookUrl}\n`);
for (const [clientId, eventTypes] of Object.entries(result.byClient)) {
  console.log(`${clientId}  ${eventTypes.length} event types`);
  for (const type of eventTypes) console.log(`            ${type}`);
}
if (result.failed.length > 0) {
  console.error('\nfailures:', result.failed);
  process.exitCode = 1;
}
if (result.secretsHandedToReceiver > 0) {
  console.log(`\nhanded ${result.secretsHandedToReceiver} signing secrets to the demo receiver`);
}

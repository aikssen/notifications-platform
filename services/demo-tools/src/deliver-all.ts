/**
 * CLI wrapper. The same action is available as POST /simulate/deliver-all, so
 * the demo can run entirely from Postman; both call into `simulate.ts`.
 */
import { env } from './fixture.js';
import { closeProducer, deliverAll } from './simulate.js';

const messages = await deliverAll({
  brokers: env('KAFKA_BROKERS', 'localhost:9092').split(','),
  topic: env('KAFKA_TOPIC_DISPATCH', 'notifications.dispatch'),
});

console.log(`published ${messages.length} events`);
for (const m of messages) {
  console.log(`  ${m.event_id}  ${m.client_id}  ${m.event_type}`);
}
console.log('\nwatch them arrive on the operations dashboard');

await closeProducer();

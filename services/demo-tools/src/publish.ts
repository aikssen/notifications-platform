/**
 * Publishes one ad-hoc event, for showing a single delivery end to end.
 *
 *   pnpm publish -- --client CLIENT001 --type credit_card_payment
 */
import { randomUUID } from 'node:crypto';
import { parseArgs } from 'node:util';

import { Kafka } from 'kafkajs';

import { env } from './fixture.js';

async function main(): Promise<void> {
  const { values } = parseArgs({
    options: {
      client: { type: 'string', default: 'CLIENT001' },
      type: { type: 'string', default: 'credit_card_payment' },
      'event-id': { type: 'string' },
      content: { type: 'string', default: 'Credit card payment received for $150.00' },
    },
  });

  const message = {
    // A fresh id each run, so repeated demos are not swallowed by the
    // idempotency constraint. Passing --event-id shows the opposite: the
    // second publish of the same id creates nothing new.
    event_id: values['event-id'] ?? `EVT-${randomUUID().slice(0, 8).toUpperCase()}`,
    client_id: values.client,
    event_type: values.type,
    event_payload: { content: values.content },
    dispatch_source: 'SYSTEM',
  };

  const kafka = new Kafka({
    clientId: 'demo-tools',
    brokers: env('KAFKA_BROKERS', 'localhost:9092').split(','),
  });
  const producer = kafka.producer();
  await producer.connect();

  await producer.send({
    topic: env('KAFKA_TOPIC_DISPATCH', 'notifications.dispatch'),
    messages: [{ key: message.client_id, value: JSON.stringify(message) }],
  });

  console.log('published', JSON.stringify(message, null, 2));
  await producer.disconnect();
}

main().catch((err: unknown) => {
  console.error('publish failed', err);
  process.exit(1);
});

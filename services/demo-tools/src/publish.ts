/**
 * CLI wrapper. Also available as POST /simulate/publish.
 *
 *   pnpm run publish-event --client CLIENT001 --type credit_card_payment
 */
import { parseArgs } from 'node:util';

import { env } from './fixture.js';
import { closeProducer, publishOne } from './simulate.js';

const { values } = parseArgs({
  // pnpm forwards the `--` separator through to the script, so positionals
  // have to be tolerated rather than rejected.
  allowPositionals: true,
  options: {
    client: { type: 'string', default: 'CLIENT001' },
    type: { type: 'string', default: 'credit_card_payment' },
    'event-id': { type: 'string' },
    content: { type: 'string' },
  },
});

const message = await publishOne(
  {
    brokers: env('KAFKA_BROKERS', 'localhost:9092').split(','),
    topic: env('KAFKA_TOPIC_DISPATCH', 'notifications.dispatch'),
  },
  {
    clientId: values.client ?? 'CLIENT001',
    eventType: values.type ?? 'credit_card_payment',
    ...(values['event-id'] ? { eventId: values['event-id'] } : {}),
    ...(values.content ? { content: values.content } : {}),
  },
);

console.log('published', JSON.stringify(message, null, 2));
await closeProducer();

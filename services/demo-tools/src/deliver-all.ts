/**
 * Publishes every event in `fixtures/notification_events.json` onto the
 * delivery topic, so the whole fixture travels the real pipeline to whatever
 * webhook the subscriptions point at.
 *
 *   "obtain the list of notifications from a repository (the attached file
 *    named: notification_events.json) and deliver them via a webhook to a
 *    public https URL"
 *
 * This is that sentence, executed. It stands in for the platform's own
 * producers: nothing here knows a webhook exists.
 */
import { Kafka } from 'kafkajs';

import { env, loadFixture } from './fixture.js';

async function main(): Promise<void> {
  const brokers = env('KAFKA_BROKERS', 'localhost:9092').split(',');
  const topic = env('KAFKA_TOPIC_DISPATCH', 'notifications.dispatch');

  const events = await loadFixture();

  const kafka = new Kafka({ clientId: 'demo-tools', brokers });
  const producer = kafka.producer();
  await producer.connect();

  await producer.send({
    topic,
    messages: events.map((event) => ({
      // Keyed by client, so one client's events stay ordered relative to each
      // other even while the dispatcher consumes partitions in parallel.
      key: event.client_id,
      value: JSON.stringify({
        event_id: event.event_id,
        client_id: event.client_id,
        event_type: event.event_type,
        event_payload: { content: event.content },
        // These come from the platform, so they are first deliveries.
        dispatch_source: 'SYSTEM',
      }),
    })),
  });

  console.log(`published ${events.length} events to ${topic}`);
  for (const event of events) {
    console.log(`  ${event.event_id}  ${event.client_id}  ${event.event_type}`);
  }
  console.log('\nwatch them arrive on the operations dashboard');

  await producer.disconnect();
}

main().catch((err: unknown) => {
  console.error('deliver-all failed', err);
  process.exit(1);
});

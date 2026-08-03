import { Kafka, logLevel, type Consumer } from 'kafkajs';
import type { Logger } from 'pino';

import type { DeliveryResult, DeliveryStatus, DispatchSource } from '../../domain/delivery-feed.js';
import type { ResultStream } from '../../application/ports.js';

/**
 * Consumes `notifications.delivery-result`.
 *
 * This is the architectural point of the whole observability story: the
 * monitor is just another consumer of the event stream. It can be restarted,
 * scaled, replaced or switched off and the delivery path does not notice —
 * because nothing in the delivery path knows it exists.
 *
 * A fresh consumer group per instance, reading from the end: this is a live
 * window, not a ledger, and two dashboards should both see everything rather
 * than splitting the stream between them.
 */
export class KafkaResultStream implements ResultStream {
  private readonly consumer: Consumer;

  constructor(
    brokers: string[],
    private readonly topic: string,
    groupId: string,
    private readonly log: Logger,
  ) {
    const kafka = new Kafka({
      clientId: 'monitor-service',
      brokers,
      // WARN keeps connection failures and drops the connect/rebalance narration.
      logLevel: logLevel.WARN,
    });
    this.consumer = kafka.consumer({ groupId });
  }

  async start(handler: (result: DeliveryResult) => void): Promise<void> {
    await this.consumer.connect();
    await this.consumer.subscribe({ topic: this.topic, fromBeginning: false });

    await this.consumer.run({
      eachMessage: async ({ message }) => {
        if (!message.value) return;
        try {
          handler(decode(JSON.parse(message.value.toString()) as Record<string, unknown>));
        } catch (err) {
          // A malformed result must never stop the feed. The delivery itself
          // already happened and is recorded in PostgreSQL; losing one line on
          // a dashboard is not worth halting the consumer for.
          this.log.warn({ err }, 'skipping an unreadable delivery result');
        }
      },
    });

    this.log.info({ topic: this.topic }, 'consuming delivery results');
  }

  async stop(): Promise<void> {
    await this.consumer.disconnect();
  }
}

function decode(raw: Record<string, unknown>): DeliveryResult {
  return {
    notificationEventId: String(raw.notification_event_id ?? ''),
    eventId: String(raw.event_id ?? ''),
    clientId: String(raw.client_id ?? ''),
    eventType: String(raw.event_type ?? ''),
    state: String(raw.state ?? ''),
    status: (raw.status as DeliveryStatus) ?? 'FAILED',
    dispatchSource: (raw.dispatch_source as DispatchSource) ?? 'SYSTEM',
    attemptNumber: Number(raw.attempt_number ?? 0),
    webhookUrl: String(raw.webhook_url ?? ''),
    responseStatus: raw.response_status == null ? null : Number(raw.response_status),
    errorMessage: raw.error_message == null ? null : String(raw.error_message),
    durationMs: Number(raw.duration_ms ?? 0),
    correlationId: raw.correlation_id == null ? null : String(raw.correlation_id),
    occurredAt: raw.occurred_at ? new Date(String(raw.occurred_at)) : new Date(),
  };
}

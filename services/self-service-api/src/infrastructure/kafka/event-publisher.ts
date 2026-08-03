import type { Producer } from 'kafkajs';

import type { DispatchMessage, EventPublisher } from '../../application/ports.js';

/**
 * Publishes a replay onto the same topic a first delivery arrives on.
 *
 * Keyed by client id, which keeps one client's events on one partition and so
 * preserves their relative order while still letting the dispatcher scale
 * across partitions.
 */
export class KafkaEventPublisher implements EventPublisher {
  constructor(
    private readonly producer: Producer,
    private readonly topic: string,
  ) {}

  async publish(message: DispatchMessage): Promise<void> {
    await this.producer.send({
      topic: this.topic,
      messages: [
        {
          key: message.client_id,
          value: JSON.stringify(message),
        },
      ],
    });
  }
}

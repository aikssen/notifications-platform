import { DomainError } from '../../domain/notification-event.js';
import type { Clock, EventPublisher, NotificationEventRepository } from '../ports.js';

export interface ReplayResult {
  notificationEventId: string;
  clientId: string;
  state: 'RETRYING';
  message: string;
}

/**
 * "Re-send a notification when delivery has definitely failed."
 *
 * The replay does not deliver anything itself. It puts the original event back
 * on the same Kafka topic a first delivery arrives on, tagged
 * `dispatch_source = SELF_SERVICE`.
 *
 * That is the whole architectural point of the platform in one use case: a
 * first delivery, an automatic retry and a client-requested replay are the
 * same code path. Only the origin differs, and it differs only so the audit
 * trail can tell them apart. There is no second implementation of delivery
 * that could drift from the first.
 */
export class ReplayNotificationEvent {
  constructor(
    private readonly repository: NotificationEventRepository,
    private readonly publisher: EventPublisher,
    private readonly clock: Clock,
  ) {}

  async execute(input: {
    notificationEventId: string;
    clientId: string;
    correlationId?: string;
  }): Promise<ReplayResult> {
    const found = await this.repository.findByIdForClient(
      input.notificationEventId,
      input.clientId,
    );

    // Not found and not yours are the same answer, on purpose.
    if (!found) {
      throw new DomainError('notification event not found', 'EVENT_NOT_FOUND');
    }

    const { event } = found;

    if (!this.repository.markForReplay) {
      throw new DomainError(
        'this deployment is running against a read-only event source',
        'REPLAY_NOT_SUPPORTED',
      );
    }

    // Throws EVENT_NOT_REPLAYABLE unless the event definitively failed.
    event.markForReplay(this.clock.now());

    // The state is moved first. If the publish then fails the event is left in
    // RETRYING, which the retry worker picks up on its next pass — so a lost
    // publish degrades into a slightly later retry rather than a replay the
    // client was told happened and never did.
    await this.repository.markForReplay(event);

    await this.publisher.publish({
      event_id: event.eventId,
      client_id: event.clientId,
      event_type: event.eventType,
      event_payload: event.payload,
      dispatch_source: 'SELF_SERVICE',
      ...(input.correlationId ? { correlation_id: input.correlationId } : {}),
    });

    return {
      notificationEventId: event.id,
      clientId: event.clientId,
      state: 'RETRYING',
      message: 'Replay scheduled',
    };
  }
}

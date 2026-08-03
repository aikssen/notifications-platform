import { v5 as uuidv5 } from 'uuid';

import type { DeliveryStatus, EventState, NotificationAttempt } from '../../domain/notification-event.js';

/**
 * The shape of `fixtures/notification_events.json`, exactly as the case
 * statement attached it.
 */
export interface FixtureEvent {
  event_id: string;
  event_type: string;
  content: string;
  delivery_date: string;
  delivery_status: string;
  client_id: string;
}

export interface FixtureFile {
  events: FixtureEvent[];
}

/**
 * A fixed namespace, so `EVT001` maps to the same notification_event_id every
 * time — in this adapter, in the seeder, and across restarts. Without a stable
 * derivation, the identifier a client saw yesterday would not resolve today.
 */
const NAMESPACE = '6f0b3b96-6c2a-4f2c-9a17-6a3c2ee6c8b1';

export function notificationEventIdFor(eventId: string): string {
  return uuidv5(eventId, NAMESPACE);
}

/**
 * The fixture speaks the case statement's vocabulary; the platform speaks its
 * own. Mapping the two is a boundary concern, so it lives here rather than
 * bending the domain to the shape of a sample file.
 *
 * The three `failed` events are the ones the replay requirement acts on.
 */
export function toEventState(deliveryStatus: string): EventState {
  switch (deliveryStatus.toLowerCase()) {
    case 'completed':
      return 'DELIVERED';
    case 'failed':
      return 'FAILED';
    case 'pending':
      return 'PENDING';
    default:
      return 'PENDING';
  }
}

export function toDeliveryStatus(deliveryStatus: string): DeliveryStatus {
  return toEventState(deliveryStatus) === 'DELIVERED' ? 'SUCCESS' : 'FAILED';
}

/**
 * The fixture records an outcome but no delivery history, so one attempt is
 * synthesised from it. The seeder writes the same thing into PostgreSQL, which
 * is what lets the two adapters answer identically.
 */
export function synthesiseAttempt(event: FixtureEvent, webhookUrl: string): NotificationAttempt {
  const succeeded = toEventState(event.delivery_status) === 'DELIVERED';

  return {
    attemptNumber: 1,
    dispatchSource: 'SYSTEM',
    status: succeeded ? 'SUCCESS' : 'FAILED',
    webhookUrl,
    requestMethod: 'POST',
    requestPayload: { content: event.content },
    responseStatus: succeeded ? 200 : 500,
    responseBody: succeeded ? { status: 'received' } : { error: 'internal_error' },
    errorMessage: succeeded ? null : 'Webhook returned 500',
    durationMs: null,
    attemptedAt: new Date(event.delivery_date),
  };
}

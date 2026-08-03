/**
 * Every edge of the self-service API. Four outbound ports.
 *
 * NotificationEventRepository has two adapters — PostgreSQL and the JSON
 * fixture attached to the case statement — and the API behaves identically
 * against either. Switching between them is one environment variable, which is
 * the most direct demonstration that the boundary is real and not decorative.
 */

import type {
  NotificationAttempt,
  NotificationEvent,
  NotificationEventFilters,
  Page,
} from '../domain/notification-event.js';

export interface NotificationEventDetail {
  event: NotificationEvent;
  attempts: NotificationAttempt[];
}

export interface NotificationEventRepository {
  /** Always scoped by client — the filters carry the caller's identity. */
  findPage(filters: NotificationEventFilters): Promise<Page<NotificationEvent>>;

  /**
   * Looks up one event *for a specific client*. Taking the client id here
   * rather than filtering afterwards means an ownership check cannot be
   * forgotten at a call site.
   */
  findByIdForClient(id: string, clientId: string): Promise<NotificationEventDetail | null>;

  /** Persists a replay request. Optional: the read-only file adapter omits it. */
  markForReplay?(event: NotificationEvent): Promise<void>;
}

/** The wire contract of the delivery topic — identical to what the platform's own producers publish. */
export interface DispatchMessage {
  event_id: string;
  client_id: string;
  event_type: string;
  event_payload: unknown;
  dispatch_source: 'SELF_SERVICE';
  correlation_id?: string;
}

export interface EventPublisher {
  publish(message: DispatchMessage): Promise<void>;
}

export interface Clock {
  now(): Date;
}

export interface IdGenerator {
  newId(): string;
}

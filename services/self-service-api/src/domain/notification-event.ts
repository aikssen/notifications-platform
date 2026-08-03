/**
 * The client-facing view of a notification event and its delivery history.
 *
 * This service reads what the dispatcher and the retry worker wrote. It owns
 * exactly one rule of its own — when a client may ask for a redelivery — and
 * that rule comes straight from the case statement:
 *
 *   "Re-send a notification when delivery has definitely failed."
 */

export const EVENT_STATES = [
  'PENDING',
  'DELIVERING',
  'RETRYING',
  'DELIVERED',
  'FAILED',
] as const;
export type EventState = (typeof EVENT_STATES)[number];

export const DELIVERY_STATUSES = ['SUCCESS', 'FAILED'] as const;
export type DeliveryStatus = (typeof DELIVERY_STATUSES)[number];

export const DISPATCH_SOURCES = ['SYSTEM', 'RETRY_SERVICE', 'SELF_SERVICE'] as const;
export type DispatchSource = (typeof DISPATCH_SOURCES)[number];

export class DomainError extends Error {
  constructor(
    message: string,
    readonly code: string,
  ) {
    super(message);
    this.name = 'DomainError';
  }
}

export interface NotificationAttempt {
  attemptNumber: number;
  dispatchSource: DispatchSource;
  status: DeliveryStatus;
  webhookUrl: string;
  requestMethod: string;
  requestPayload: unknown;
  responseStatus: number | null;
  responseBody: unknown;
  errorMessage: string | null;
  durationMs: number | null;
  attemptedAt: Date;
}

export interface NotificationEventProps {
  id: string;
  eventId: string;
  clientId: string;
  eventType: string;
  payload: unknown;
  state: EventState;
  retryCount: number;
  lastError: string | null;
  createdAt: Date;
  updatedAt: Date;
}

export class NotificationEvent {
  constructor(private readonly props: NotificationEventProps) {}

  get id(): string {
    return this.props.id;
  }
  get eventId(): string {
    return this.props.eventId;
  }
  get clientId(): string {
    return this.props.clientId;
  }
  get eventType(): string {
    return this.props.eventType;
  }
  get payload(): unknown {
    return this.props.payload;
  }
  get state(): EventState {
    return this.props.state;
  }
  get retryCount(): number {
    return this.props.retryCount;
  }
  get lastError(): string | null {
    return this.props.lastError;
  }
  get createdAt(): Date {
    return this.props.createdAt;
  }
  get updatedAt(): Date {
    return this.props.updatedAt;
  }

  belongsTo(clientId: string): boolean {
    return this.props.clientId === clientId;
  }

  /**
   * A client may only replay a delivery that definitively failed.
   *
   * The previous implementation had this guard inverted: it rejected FAILED
   * and accepted PENDING and RETRYING — the exact opposite of the requirement.
   * It went unnoticed because the dispatcher never wrote FAILED either, so the
   * branch was unreachable and nothing ever contradicted it.
   */
  canBeReplayed(): boolean {
    return this.props.state === 'FAILED';
  }

  /** Explains a refusal in the terms the client experiences. */
  replayRefusalReason(): string {
    switch (this.props.state) {
      case 'DELIVERED':
        return 'This notification was delivered successfully and will not be sent again.';
      case 'PENDING':
      case 'DELIVERING':
        return 'This notification is still being delivered. Wait for it to settle before replaying.';
      case 'RETRYING':
        return 'This notification is already scheduled for another automatic attempt.';
      default:
        return 'This notification cannot be replayed in its current state.';
    }
  }

  /**
   * Hands the event back to the delivery pipeline.
   *
   * Note that it becomes RETRYING, not PENDING: a replay re-enters the exact
   * same lifecycle an automatic retry uses. One pipeline, three origins.
   */
  markForReplay(now: Date): void {
    if (!this.canBeReplayed()) {
      throw new DomainError(this.replayRefusalReason(), 'EVENT_NOT_REPLAYABLE');
    }
    this.props.state = 'RETRYING';
    this.props.updatedAt = now;
  }
}

/** The list filters the case statement asks for, plus pagination. */
export interface NotificationEventFilters {
  clientId: string;
  state?: EventState;
  createdFrom?: Date;
  createdTo?: Date;
  page: number;
  pageSize: number;
}

export interface Page<T> {
  items: T[];
  page: number;
  pageSize: number;
  total: number;
}

/**
 * A subscription is the delivery contract a client registered: which of their
 * event types goes where, over which method, and what response counts as
 * accepted.
 *
 * It is the object that makes the case statement's hardest requirement
 * enforceable — "it's mandatory to ensure notifications sent to every client
 * belong to events generated to that client" — because resolution is always
 * keyed on client and event type together, never on event type alone.
 */

export const SUBSCRIPTION_STATUS = ['ACTIVE', 'INACTIVE'] as const;
export type SubscriptionStatus = (typeof SUBSCRIPTION_STATUS)[number];

export const HTTP_METHODS = ['POST', 'PUT', 'PATCH'] as const;
export type HttpMethod = (typeof HTTP_METHODS)[number];

export class DomainError extends Error {
  constructor(
    message: string,
    readonly code: string,
  ) {
    super(message);
    this.name = 'DomainError';
  }
}

export interface SubscriptionProps {
  id: string;
  clientId: string;
  eventType: string;
  webhookUrl: string;
  httpMethod: HttpMethod;
  expectedStatus: number;
  hmacSecret: string;
  status: SubscriptionStatus;
  createdAt: Date;
  updatedAt: Date;
}

export class Subscription {
  private constructor(private readonly props: SubscriptionProps) {}

  static create(input: {
    id: string;
    clientId: string;
    eventType: string;
    webhookUrl: string;
    httpMethod: HttpMethod;
    expectedStatus: number;
    hmacSecret: string;
    now: Date;
  }): Subscription {
    if (!input.clientId) {
      throw new DomainError('client_id is required', 'MISSING_CLIENT_ID');
    }
    if (!input.eventType.trim()) {
      throw new DomainError('event_type is required', 'MISSING_EVENT_TYPE');
    }
    if (!Number.isInteger(input.expectedStatus) || input.expectedStatus < 100 || input.expectedStatus > 599) {
      throw new DomainError(
        'expected_status must be a valid HTTP status code',
        'INVALID_EXPECTED_STATUS',
      );
    }
    if (input.hmacSecret.length < 32) {
      throw new DomainError(
        'the signing secret is too short to be safe',
        'WEAK_SECRET',
      );
    }

    // Only the syntactic rules live here. Whether the host resolves to a
    // private address needs DNS, which is I/O, so it belongs to an adapter
    // behind the WebhookUrlValidator port.
    assertSyntacticallyValidWebhookUrl(input.webhookUrl);

    return new Subscription({
      id: input.id,
      clientId: input.clientId,
      eventType: input.eventType.trim(),
      webhookUrl: input.webhookUrl,
      httpMethod: input.httpMethod,
      expectedStatus: input.expectedStatus,
      hmacSecret: input.hmacSecret,
      status: 'ACTIVE',
      createdAt: input.now,
      updatedAt: input.now,
    });
  }

  static rehydrate(props: SubscriptionProps): Subscription {
    return new Subscription(props);
  }

  get id(): string {
    return this.props.id;
  }
  get clientId(): string {
    return this.props.clientId;
  }
  get eventType(): string {
    return this.props.eventType;
  }
  get webhookUrl(): string {
    return this.props.webhookUrl;
  }
  get httpMethod(): HttpMethod {
    return this.props.httpMethod;
  }
  get expectedStatus(): number {
    return this.props.expectedStatus;
  }
  get hmacSecret(): string {
    return this.props.hmacSecret;
  }
  get status(): SubscriptionStatus {
    return this.props.status;
  }
  get createdAt(): Date {
    return this.props.createdAt;
  }
  get updatedAt(): Date {
    return this.props.updatedAt;
  }

  isActive(): boolean {
    return this.props.status === 'ACTIVE';
  }

  /** Ownership check, used before anything is returned or mutated. */
  belongsTo(clientId: string): boolean {
    return this.props.clientId === clientId;
  }

  deactivate(now: Date): void {
    this.props.status = 'INACTIVE';
    this.props.updatedAt = now;
  }
}

/**
 * Syntactic webhook URL rules — everything that can be decided without a
 * network lookup.
 */
export function assertSyntacticallyValidWebhookUrl(raw: string): URL {
  let url: URL;
  try {
    url = new URL(raw);
  } catch {
    throw new DomainError('webhook_url is not a valid URL', 'INVALID_WEBHOOK_URL');
  }

  if (url.protocol !== 'https:' && url.protocol !== 'http:') {
    throw new DomainError(
      `webhook_url scheme ${url.protocol} is not allowed`,
      'INVALID_WEBHOOK_SCHEME',
    );
  }

  // Credentials in a webhook URL end up in logs, in the database and in every
  // error message that quotes the destination.
  if (url.username || url.password) {
    throw new DomainError(
      'webhook_url must not contain credentials',
      'CREDENTIALS_IN_WEBHOOK_URL',
    );
  }

  if (!url.hostname) {
    throw new DomainError('webhook_url has no host', 'INVALID_WEBHOOK_URL');
  }

  return url;
}

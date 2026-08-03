import { Subscription, DomainError, type HttpMethod } from '../../domain/subscription.js';
import type {
  Clock,
  IdGenerator,
  SecretGenerator,
  SubscriptionRepository,
  WebhookUrlValidator,
} from '../ports.js';

export interface CreateSubscriptionCommand {
  /**
   * Always taken from the authenticated token, never from the request body.
   * Accepting a client_id from the payload is how a multi-tenant API ends up
   * letting one client register a webhook against another's events.
   */
  clientId: string;
  webhookUrl: string;
  httpMethod: HttpMethod;
  expectedStatus: number;
  eventTypes: string[];
}

export interface CreatedSubscription {
  subscription: Subscription;
  /** Returned exactly once, at creation. It is never readable again. */
  secret: string;
}

export class CreateSubscription {
  constructor(
    private readonly repository: SubscriptionRepository,
    private readonly webhookUrls: WebhookUrlValidator,
    private readonly secrets: SecretGenerator,
    private readonly ids: IdGenerator,
    private readonly clock: Clock,
  ) {}

  async execute(command: CreateSubscriptionCommand): Promise<CreatedSubscription[]> {
    if (command.eventTypes.length === 0) {
      throw new DomainError('at least one event type is required', 'NO_EVENT_TYPES');
    }

    // Validated once, before anything is written. A rejected destination must
    // not leave half a batch of subscriptions behind.
    await this.webhookUrls.assertAllowed(command.webhookUrl);

    const now = this.clock.now();
    const created: CreatedSubscription[] = [];

    for (const eventType of dedupe(command.eventTypes)) {
      const existing = await this.repository.findActiveByClientAndEventType(
        command.clientId,
        eventType,
      );
      if (existing) {
        throw new DomainError(
          `an active subscription already exists for ${eventType}`,
          'SUBSCRIPTION_ALREADY_EXISTS',
        );
      }

      const secret = this.secrets.generate();
      const subscription = Subscription.create({
        id: this.ids.newId(),
        clientId: command.clientId,
        eventType,
        webhookUrl: command.webhookUrl,
        httpMethod: command.httpMethod,
        expectedStatus: command.expectedStatus,
        hmacSecret: secret,
        now,
      });

      await this.repository.save(subscription);
      created.push({ subscription, secret });
    }

    return created;
  }
}

function dedupe(values: string[]): string[] {
  return [...new Set(values.map((v) => v.trim()).filter(Boolean))];
}

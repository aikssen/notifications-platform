import { DomainError, type Subscription } from '../../domain/subscription.js';
import type { Clock, SubscriptionRepository } from '../ports.js';

export class ListSubscriptions {
  constructor(private readonly repository: SubscriptionRepository) {}

  execute(clientId: string): Promise<Subscription[]> {
    return this.repository.findByClient(clientId);
  }
}

/**
 * Resolution for the dispatcher. This is the check the case statement calls
 * mandatory: an event is only deliverable if the client it claims to belong to
 * actually registered a webhook for that event type.
 */
export class ResolveSubscription {
  constructor(private readonly repository: SubscriptionRepository) {}

  execute(clientId: string, eventType: string): Promise<Subscription | null> {
    return this.repository.findActiveByClientAndEventType(clientId, eventType);
  }
}

export class DeleteSubscription {
  constructor(
    private readonly repository: SubscriptionRepository,
    private readonly clock: Clock,
  ) {}

  async execute(id: string, clientId: string): Promise<void> {
    const subscription = await this.repository.findById(id);

    // Ownership failure is reported as "not found", not "forbidden". Telling a
    // caller that a resource exists but is not theirs turns the endpoint into
    // an enumeration oracle.
    if (!subscription || !subscription.belongsTo(clientId)) {
      throw new DomainError('subscription not found', 'SUBSCRIPTION_NOT_FOUND');
    }

    subscription.deactivate(this.clock.now());
    await this.repository.delete(subscription.id);
  }
}

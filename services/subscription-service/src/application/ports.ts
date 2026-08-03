/**
 * Every edge of the subscription service. Five outbound ports, and nothing in
 * this file mentions PostgreSQL, Express or DNS.
 *
 * The dependency rule is enforced by `import/no-restricted-paths` and
 * `no-restricted-imports` in the workspace ESLint config: importing `pg` from
 * here, or from anything under `domain/`, fails `pnpm lint`.
 */

import type { Subscription } from '../domain/subscription.js';

export interface SubscriptionRepository {
  save(subscription: Subscription): Promise<void>;

  /**
   * The resolution query the dispatcher calls before every delivery. Keyed on
   * both client and event type, which is what enforces tenant isolation at the
   * delivery layer.
   */
  findActiveByClientAndEventType(
    clientId: string,
    eventType: string,
  ): Promise<Subscription | null>;

  findByClient(clientId: string): Promise<Subscription[]>;
  findById(id: string): Promise<Subscription | null>;
  delete(id: string): Promise<void>;
}

/**
 * WebhookUrlValidator is the SSRF guard (OWASP A10).
 *
 * It is a port rather than a domain rule because deciding whether a hostname
 * points somewhere dangerous requires resolving it, and DNS is I/O.
 */
export interface WebhookUrlValidator {
  /** Rejects with a {@link WebhookUrlRejected} when the destination is not allowed. */
  assertAllowed(url: string): Promise<void>;
}

export class WebhookUrlRejected extends Error {
  constructor(
    message: string,
    readonly code: string,
  ) {
    super(message);
    this.name = 'WebhookUrlRejected';
  }
}

export interface SecretGenerator {
  /** Cryptographically strong per-subscription signing secret. */
  generate(): string;
}

export interface IdGenerator {
  newId(): string;
}

export interface Clock {
  now(): Date;
}

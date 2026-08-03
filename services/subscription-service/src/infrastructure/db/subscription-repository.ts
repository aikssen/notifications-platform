import type { Pool } from 'pg';

import { Subscription, type HttpMethod, type SubscriptionStatus } from '../../domain/subscription.js';
import type { SubscriptionRepository } from '../../application/ports.js';

interface SubscriptionRow {
  id: string;
  client_id: string;
  event_type: string;
  webhook_url: string;
  http_method: string;
  expected_status: number;
  hmac_secret: string;
  status: string;
  created_at: Date;
  updated_at: Date;
}

export class PostgresSubscriptionRepository implements SubscriptionRepository {
  constructor(private readonly pool: Pool) {}

  async save(subscription: Subscription): Promise<void> {
    await this.pool.query(
      `INSERT INTO subscriptions (
         id, client_id, event_type, webhook_url, http_method,
         expected_status, hmac_secret, status, created_at, updated_at
       ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
       ON CONFLICT (client_id, event_type) DO UPDATE SET
         webhook_url     = EXCLUDED.webhook_url,
         http_method     = EXCLUDED.http_method,
         expected_status = EXCLUDED.expected_status,
         hmac_secret     = EXCLUDED.hmac_secret,
         status          = EXCLUDED.status,
         updated_at      = EXCLUDED.updated_at`,
      [
        subscription.id,
        subscription.clientId,
        subscription.eventType,
        subscription.webhookUrl,
        subscription.httpMethod,
        subscription.expectedStatus,
        subscription.hmacSecret,
        subscription.status,
        subscription.createdAt,
        subscription.updatedAt,
      ],
    );
  }

  /**
   * The query the dispatcher runs before every delivery. Both predicates are
   * mandatory: dropping `client_id` would turn a subscription lookup into a
   * cross-tenant delivery.
   */
  async findActiveByClientAndEventType(
    clientId: string,
    eventType: string,
  ): Promise<Subscription | null> {
    const result = await this.pool.query<SubscriptionRow>(
      `SELECT * FROM subscriptions
       WHERE client_id = $1 AND event_type = $2 AND status = 'ACTIVE'
       LIMIT 1`,
      [clientId, eventType],
    );
    const row = result.rows[0];
    return row ? toDomain(row) : null;
  }

  async findByClient(clientId: string): Promise<Subscription[]> {
    const result = await this.pool.query<SubscriptionRow>(
      `SELECT * FROM subscriptions WHERE client_id = $1 ORDER BY created_at DESC`,
      [clientId],
    );
    return result.rows.map(toDomain);
  }

  async findById(id: string): Promise<Subscription | null> {
    const result = await this.pool.query<SubscriptionRow>(
      `SELECT * FROM subscriptions WHERE id = $1`,
      [id],
    );
    const row = result.rows[0];
    return row ? toDomain(row) : null;
  }

  /**
   * Deactivates rather than deletes. A delivery that failed last week must
   * still be explainable next month, and its audit trail references the
   * webhook this row described.
   */
  async delete(id: string): Promise<void> {
    await this.pool.query(
      `UPDATE subscriptions SET status = 'INACTIVE', updated_at = now() WHERE id = $1`,
      [id],
    );
  }
}

/**
 * Rows are snake_case; the domain is camelCase. Mapping them explicitly is
 * dull and it is also where the previous implementation broke: it read
 * `row.webhookUrl` from a snake_case row, so those fields silently came back
 * undefined in the API response.
 */
function toDomain(row: SubscriptionRow): Subscription {
  return Subscription.rehydrate({
    id: row.id,
    clientId: row.client_id,
    eventType: row.event_type,
    webhookUrl: row.webhook_url,
    httpMethod: row.http_method as HttpMethod,
    expectedStatus: row.expected_status,
    hmacSecret: row.hmac_secret,
    status: row.status as SubscriptionStatus,
    createdAt: row.created_at,
    updatedAt: row.updated_at,
  });
}

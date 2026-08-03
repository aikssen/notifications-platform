import type { Pool } from 'pg';

import {
  NotificationEvent,
  type DeliveryStatus,
  type DispatchSource,
  type EventState,
  type NotificationAttempt,
  type NotificationEventFilters,
  type Page,
} from '../../domain/notification-event.js';
import type { NotificationEventDetail, NotificationEventRepository } from '../../application/ports.js';

interface EventRow {
  id: string;
  event_id: string;
  client_id: string;
  event_type: string;
  event_payload: unknown;
  state: string;
  retry_count: number;
  last_error: string | null;
  created_at: Date;
  updated_at: Date;
}

interface AttemptRow {
  attempt_number: number;
  dispatch_source: string;
  status: string;
  webhook_url: string;
  request_method: string;
  request_payload: unknown;
  response_status: number | null;
  response_body: unknown;
  error_message: string | null;
  duration_ms: number | null;
  attempted_at: Date;
}

export class PostgresNotificationEventRepository implements NotificationEventRepository {
  constructor(private readonly pool: Pool) {}

  /**
   * Every query is parameterised (OWASP A03). The filters are optional, so the
   * WHERE clause is assembled from a fixed set of fragments and a positional
   * parameter list — never by concatenating caller input into SQL.
   */
  async findPage(filters: NotificationEventFilters): Promise<Page<NotificationEvent>> {
    const conditions: string[] = ['client_id = $1'];
    const params: unknown[] = [filters.clientId];

    if (filters.state) {
      params.push(filters.state);
      conditions.push(`state = $${params.length}`);
    }
    if (filters.createdFrom) {
      params.push(filters.createdFrom);
      conditions.push(`created_at >= $${params.length}`);
    }
    if (filters.createdTo) {
      params.push(filters.createdTo);
      conditions.push(`created_at <= $${params.length}`);
    }

    const where = conditions.join(' AND ');

    const totalResult = await this.pool.query<{ count: string }>(
      `SELECT COUNT(*)::text AS count FROM notification_events WHERE ${where}`,
      params,
    );

    const offset = (filters.page - 1) * filters.pageSize;
    const rows = await this.pool.query<EventRow>(
      `SELECT id, event_id, client_id, event_type, event_payload,
              state, retry_count, last_error, created_at, updated_at
       FROM notification_events
       WHERE ${where}
       ORDER BY created_at DESC, id DESC
       LIMIT $${params.length + 1} OFFSET $${params.length + 2}`,
      [...params, filters.pageSize, offset],
    );

    return {
      items: rows.rows.map(toEvent),
      page: filters.page,
      pageSize: filters.pageSize,
      total: Number(totalResult.rows[0]?.count ?? 0),
    };
  }

  /** The client id is part of the WHERE clause, not an afterthought. */
  async findByIdForClient(id: string, clientId: string): Promise<NotificationEventDetail | null> {
    const eventResult = await this.pool.query<EventRow>(
      `SELECT id, event_id, client_id, event_type, event_payload,
              state, retry_count, last_error, created_at, updated_at
       FROM notification_events
       WHERE id = $1 AND client_id = $2`,
      [id, clientId],
    );

    const row = eventResult.rows[0];
    if (!row) return null;

    const attempts = await this.pool.query<AttemptRow>(
      `SELECT attempt_number, dispatch_source, status, webhook_url, request_method,
              request_payload, response_status, response_body, error_message,
              duration_ms, attempted_at
       FROM notification_attempts
       WHERE notification_event_id = $1
       ORDER BY attempt_number ASC`,
      [id],
    );

    return { event: toEvent(row), attempts: attempts.rows.map(toAttempt) };
  }

  /**
   * Guarded on the state that was read. If the retry worker moved the event in
   * the meantime, this write does nothing rather than overwriting its decision.
   */
  async markForReplay(event: NotificationEvent): Promise<void> {
    await this.pool.query(
      `UPDATE notification_events
       SET state = 'RETRYING', next_retry_at = now(), updated_at = now()
       WHERE id = $1 AND client_id = $2 AND state = 'FAILED'`,
      [event.id, event.clientId],
    );
  }
}

/**
 * Rows are snake_case, the domain is camelCase.
 *
 * Writing this out is tedious and it is also exactly where the previous
 * implementation broke: it read `row.webhookUrl` off a snake_case row, so
 * webhook_url, request_method and request_payload came back undefined in the
 * detail endpoint — silently, because undefined is valid JSON output.
 */
function toEvent(row: EventRow): NotificationEvent {
  return new NotificationEvent({
    id: row.id,
    eventId: row.event_id,
    clientId: row.client_id,
    eventType: row.event_type,
    payload: row.event_payload,
    state: row.state as EventState,
    retryCount: row.retry_count,
    lastError: row.last_error,
    createdAt: row.created_at,
    updatedAt: row.updated_at,
  });
}

function toAttempt(row: AttemptRow): NotificationAttempt {
  return {
    attemptNumber: row.attempt_number,
    dispatchSource: row.dispatch_source as DispatchSource,
    status: row.status as DeliveryStatus,
    webhookUrl: row.webhook_url,
    requestMethod: row.request_method,
    requestPayload: row.request_payload,
    responseStatus: row.response_status,
    responseBody: row.response_body,
    errorMessage: row.error_message,
    durationMs: row.duration_ms,
    attemptedAt: row.attempted_at,
  };
}

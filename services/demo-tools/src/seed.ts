/**
 * Loads `fixtures/notification_events.json` as the platform's initial state.
 *
 * The file records ten events with their outcome already known: seven
 * `completed` and three `failed`. Seeding them as history rather than replaying
 * them through the pipeline is what makes the case statement's own dataset
 * exercise its own requirements — the list filters have something to filter,
 * and the replay endpoint has three definitively failed deliveries to act on.
 *
 * This script exists only for the demo. Nothing in the product writes to
 * another service's tables.
 */
import { randomUUID } from 'node:crypto';

import pg from 'pg';

import { env, loadFixture, notificationEventIdFor, toEventState } from './fixture.js';

async function main(): Promise<void> {
  const events = await loadFixture();
  const pool = new pg.Pool({ connectionString: env('DATABASE_URL') });

  const client = await pool.connect();
  try {
    await client.query('BEGIN');

    for (const raw of events) {
      const id = notificationEventIdFor(raw.event_id);
      const state = toEventState(raw.delivery_status);
      const at = new Date(raw.delivery_date);
      const failed = state === 'FAILED';

      await client.query(
        `INSERT INTO notification_events (
           id, event_id, client_id, event_type, event_payload,
           state, retry_count, last_error, created_at, updated_at
         ) VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7, $8, $9, $9)
         ON CONFLICT (event_id) DO UPDATE SET
           state = EXCLUDED.state,
           retry_count = EXCLUDED.retry_count,
           last_error = EXCLUDED.last_error,
           updated_at = EXCLUDED.updated_at`,
        [
          id,
          raw.event_id,
          raw.client_id,
          raw.event_type,
          JSON.stringify({ content: raw.content }),
          state,
          // A failed event arrives with its retry budget already spent, which
          // is what "delivery has definitely failed" means.
          failed ? Number(process.env.RETRY_MAX_ATTEMPTS ?? 5) : 0,
          failed ? 'Webhook returned 500' : null,
          at,
        ],
      );

      // One attempt per event, consistent with the recorded outcome. The file
      // adapter in self-service-api synthesises the identical shape, so both
      // repository implementations answer the same thing.
      await client.query(
        `INSERT INTO notification_attempts (
           id, notification_event_id, attempt_number, dispatch_source, status,
           webhook_url, request_method, request_payload,
           response_status, response_body, error_message, duration_ms, attempted_at
         ) VALUES ($1, $2, 1, 'SYSTEM', $3, $4, 'POST', $5::jsonb, $6, $7::jsonb, $8, $9, $10)
         ON CONFLICT (notification_event_id, attempt_number) DO NOTHING`,
        [
          randomUUID(),
          id,
          failed ? 'FAILED' : 'SUCCESS',
          process.env.FIXTURE_WEBHOOK_URL ?? 'https://client.example.com/webhooks',
          JSON.stringify({ content: raw.content }),
          failed ? 500 : 200,
          JSON.stringify(failed ? { error: 'internal_error' } : { status: 'received' }),
          failed ? 'Webhook returned 500' : null,
          failed ? 5012 : 87,
          at,
        ],
      );
    }

    await client.query('COMMIT');
  } catch (error) {
    await client.query('ROLLBACK');
    throw error;
  } finally {
    client.release();
  }

  const summary = await pool.query<{ state: string; count: string }>(
    `SELECT state, COUNT(*)::text AS count FROM notification_events GROUP BY state ORDER BY state`,
  );

  console.log(`seeded ${events.length} events from the case statement fixture`);
  for (const row of summary.rows) {
    console.log(`  ${row.state.padEnd(10)} ${row.count}`);
  }
  console.log('\nthe FAILED events are the ones the replay endpoint acts on');

  await pool.end();
}

main().catch((err: unknown) => {
  console.error('seed failed', err);
  process.exit(1);
});

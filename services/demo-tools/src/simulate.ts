/**
 * The simulation actions, as functions.
 *
 * Each one is exposed twice: as a CLI script for `make`, and as an HTTP
 * endpoint so the entire demo can be driven from Postman without switching to
 * a terminal mid-presentation. Both call the code here, so the two paths can
 * never drift.
 */
import { randomUUID } from 'node:crypto';

import { Kafka, type Producer } from 'kafkajs';
import pg from 'pg';

import { loadFixture, notificationEventIdFor, toEventState, type FixtureEvent } from './fixture.js';

// ---------------------------------------------------------------------------
// Kafka producer, connected on first use and reused afterwards
// ---------------------------------------------------------------------------

let producer: Producer | null = null;

async function getProducer(brokers: string[]): Promise<Producer> {
  if (producer) return producer;
  const kafka = new Kafka({ clientId: 'demo-tools', brokers });
  producer = kafka.producer();
  await producer.connect();
  return producer;
}

export async function closeProducer(): Promise<void> {
  if (producer) {
    await producer.disconnect();
    producer = null;
  }
}

// ---------------------------------------------------------------------------

export interface KafkaTarget {
  brokers: string[];
  topic: string;
}

export interface PublishInput {
  clientId: string;
  eventType: string;
  eventId?: string;
  content?: string;
}

export interface DispatchMessage {
  event_id: string;
  client_id: string;
  event_type: string;
  event_payload: { content: string };
  dispatch_source: 'SYSTEM';
}

/** Publishes one event, standing in for a platform microservice. */
export async function publishOne(
  target: KafkaTarget,
  input: PublishInput,
): Promise<DispatchMessage> {
  const message: DispatchMessage = {
    // A fresh id by default, so repeated demos are not swallowed by the
    // idempotency constraint. Passing an explicit event_id demonstrates the
    // opposite: publishing it twice creates nothing new.
    event_id: input.eventId ?? `EVT-${randomUUID().slice(0, 8).toUpperCase()}`,
    client_id: input.clientId,
    event_type: input.eventType,
    event_payload: {
      content: input.content ?? 'Credit card payment received for $150.00',
    },
    dispatch_source: 'SYSTEM',
  };

  const p = await getProducer(target.brokers);
  await p.send({
    topic: target.topic,
    // Keyed by client, so one client's events keep their relative order.
    messages: [{ key: message.client_id, value: JSON.stringify(message) }],
  });

  return message;
}

/**
 * Publishes every event in the case statement's fixture onto the delivery
 * topic — the brief's Task 2, executed.
 */
export async function deliverAll(target: KafkaTarget): Promise<DispatchMessage[]> {
  const events = await loadFixture();

  const messages: DispatchMessage[] = events.map((event) => ({
    event_id: event.event_id,
    client_id: event.client_id,
    event_type: event.event_type,
    event_payload: { content: event.content },
    dispatch_source: 'SYSTEM',
  }));

  const p = await getProducer(target.brokers);
  await p.send({
    topic: target.topic,
    messages: messages.map((m) => ({ key: m.client_id, value: JSON.stringify(m) })),
  });

  return messages;
}

// ---------------------------------------------------------------------------

export interface SubscribeAllInput {
  subscriptionsBaseUrl: string;
  webhookUrl: string;
  expectedStatus: number;
  /** Where to hand the signing secrets, so deliveries are actually verified. */
  receiverUrl?: string;
}

export interface SubscribeAllResult {
  registered: number;
  alreadyExisted: number;
  failed: Array<{ clientId: string; status: number; body: unknown }>;
  byClient: Record<string, string[]>;
  secretsHandedToReceiver: number;
}

/**
 * Registers a subscription for every (client, event type) pair in the fixture,
 * through the public API — so the SSRF guard runs and a signing secret is
 * issued, exactly as it would for a real client.
 */
export async function subscribeAll(input: SubscribeAllInput): Promise<SubscribeAllResult> {
  const events = await loadFixture();

  const pairs = new Map<string, Set<string>>();
  for (const event of events) {
    const forClient = pairs.get(event.client_id) ?? new Set<string>();
    forClient.add(event.event_type);
    pairs.set(event.client_id, forClient);
  }

  const result: SubscribeAllResult = {
    registered: 0,
    alreadyExisted: 0,
    failed: [],
    byClient: {},
    secretsHandedToReceiver: 0,
  };

  const secrets: Array<{ client_id: string; event_type: string; secret: string }> = [];

  for (const [clientId, eventTypes] of pairs) {
    result.byClient[clientId] = [...eventTypes];

    const token = await tokenFor(input.subscriptionsBaseUrl, clientId);

    const res = await fetch(`${input.subscriptionsBaseUrl}/subscriptions`, {
      method: 'POST',
      headers: { 'content-type': 'application/json', authorization: `Bearer ${token}` },
      body: JSON.stringify({
        webhook_url: input.webhookUrl,
        method: 'POST',
        expected_status: input.expectedStatus,
        events: [...eventTypes],
      }),
    });

    const body: unknown = await res.json().catch(() => null);

    if (res.status === 409) {
      result.alreadyExisted += eventTypes.size;
      continue;
    }
    if (!res.ok) {
      result.failed.push({ clientId, status: res.status, body });
      continue;
    }

    const issued =
      (body as { subscriptions?: Array<Record<string, string>> }).subscriptions ?? [];
    result.registered += issued.length;
    secrets.push(
      ...issued.map((s) => ({
        client_id: String(s.client_id),
        event_type: String(s.event_type),
        secret: String(s.signing_secret),
      })),
    );
  }

  if (secrets.length > 0 && input.receiverUrl) {
    const res = await fetch(`${input.receiverUrl}/secrets`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ secrets }),
    });
    if (res.ok) result.secretsHandedToReceiver = secrets.length;
  }

  return result;
}

async function tokenFor(baseUrl: string, clientId: string): Promise<string> {
  const res = await fetch(`${baseUrl}/auth/token`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ client_id: clientId }),
  });
  if (!res.ok) {
    throw new Error(
      `could not obtain a token for ${clientId}: ${res.status}. ` +
        'Is ENABLE_DEMO_TOKENS set on the subscription service?',
    );
  }
  return ((await res.json()) as { access_token: string }).access_token;
}

// ---------------------------------------------------------------------------

export interface SeedResult {
  seeded: number;
  byState: Record<string, number>;
}

/**
 * Loads the fixture as settled history — seven delivered, three failed.
 *
 * An alternative starting point to `deliverAll`, useful when the list filters
 * and the replay endpoint should be populated instantly. Writing to another
 * service's tables is acceptable here precisely because this is a demo tool
 * and not part of the product.
 */
export async function seed(databaseUrl: string, webhookUrl: string): Promise<SeedResult> {
  const events = await loadFixture();
  const pool = new pg.Pool({ connectionString: databaseUrl });

  try {
    const client = await pool.connect();
    try {
      await client.query('BEGIN');
      for (const raw of events) {
        await seedOne(client, raw, webhookUrl);
      }
      await client.query('COMMIT');
    } catch (error) {
      await client.query('ROLLBACK');
      throw error;
    } finally {
      client.release();
    }

    const summary = await pool.query<{ state: string; count: string }>(
      `SELECT state, COUNT(*)::text AS count FROM notification_events GROUP BY state`,
    );

    return {
      seeded: events.length,
      byState: Object.fromEntries(summary.rows.map((r) => [r.state, Number(r.count)])),
    };
  } finally {
    await pool.end();
  }
}

async function seedOne(
  client: pg.PoolClient,
  raw: FixtureEvent,
  webhookUrl: string,
): Promise<void> {
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
       state = EXCLUDED.state, retry_count = EXCLUDED.retry_count,
       last_error = EXCLUDED.last_error, updated_at = EXCLUDED.updated_at`,
    [
      id,
      raw.event_id,
      raw.client_id,
      raw.event_type,
      JSON.stringify({ content: raw.content }),
      state,
      // A failed event arrives with its retry budget already spent — which is
      // what "delivery has definitely failed" means.
      failed ? Number(process.env.RETRY_MAX_ATTEMPTS ?? 5) : 0,
      failed ? 'Webhook returned 500' : null,
      at,
    ],
  );

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
      webhookUrl,
      JSON.stringify({ content: raw.content }),
      failed ? 500 : 200,
      JSON.stringify(failed ? { error: 'internal_error' } : { status: 'received' }),
      failed ? 'Webhook returned 500' : null,
      failed ? 5012 : 87,
      at,
    ],
  );
}

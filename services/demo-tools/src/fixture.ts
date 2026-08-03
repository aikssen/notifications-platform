import { readFile } from 'node:fs/promises';
import { resolve } from 'node:path';

import { v5 as uuidv5 } from 'uuid';

/**
 * demo-tools stands in for two things that are not part of the product: the
 * platform microservices that emit business events, and a client's webhook
 * endpoint. Keeping them here is what lets the subscription service be a
 * subscription service rather than a mock that also produces Kafka messages
 * and answers webhooks (ADR 0003).
 *
 * These are scripts. They are deliberately not hexagonal.
 */

export interface FixtureEvent {
  event_id: string;
  event_type: string;
  content: string;
  delivery_date: string;
  delivery_status: string;
  client_id: string;
}

/**
 * Must match self-service-api's namespace exactly, or the same fixture event
 * would get one identifier in the database and a different one from the file
 * adapter.
 */
const NAMESPACE = '6f0b3b96-6c2a-4f2c-9a17-6a3c2ee6c8b1';

export function notificationEventIdFor(eventId: string): string {
  return uuidv5(eventId, NAMESPACE);
}

export function toEventState(deliveryStatus: string): 'DELIVERED' | 'FAILED' | 'PENDING' {
  switch (deliveryStatus.toLowerCase()) {
    case 'completed':
      return 'DELIVERED';
    case 'failed':
      return 'FAILED';
    default:
      return 'PENDING';
  }
}

export async function loadFixture(): Promise<FixtureEvent[]> {
  const path = resolve(
    process.cwd(),
    process.env.FIXTURE_PATH ?? '../../fixtures/notification_events.json',
  );
  const raw = await readFile(path, 'utf8');
  const parsed = JSON.parse(raw) as { events?: FixtureEvent[] };

  if (!Array.isArray(parsed.events)) {
    throw new Error(`${path} does not contain an "events" array`);
  }
  return parsed.events;
}

export function env(key: string, fallback?: string): string {
  const value = process.env[key] ?? fallback;
  if (!value) throw new Error(`missing required environment variable: ${key}`);
  return value;
}

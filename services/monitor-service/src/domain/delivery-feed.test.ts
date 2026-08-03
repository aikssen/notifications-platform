import { describe, expect, it } from 'vitest';

import { DeliveryFeed, type DeliveryResult } from './delivery-feed.js';

function result(overrides: Partial<DeliveryResult> = {}): DeliveryResult {
  return {
    notificationEventId: 'nev-1',
    eventId: 'EVT001',
    clientId: 'CLIENT001',
    eventType: 'credit_card_payment',
    state: 'DELIVERED',
    status: 'SUCCESS',
    dispatchSource: 'SYSTEM',
    attemptNumber: 1,
    webhookUrl: 'https://client.example.com/hooks',
    responseStatus: 200,
    errorMessage: null,
    durationMs: 100,
    correlationId: null,
    occurredAt: new Date('2026-03-15T09:30:22Z'),
    ...overrides,
  };
}

describe('DeliveryFeed', () => {
  it('answers the "what happened to this client" question', () => {
    const feed = new DeliveryFeed();
    feed.record(result({ clientId: 'CLIENT001' }));
    feed.record(result({ clientId: 'CLIENT002', eventId: 'EVT003' }));
    feed.record(result({ clientId: 'CLIENT001', eventId: 'EVT002' }));

    const forClient = feed.recent(10, 'CLIENT001');
    expect(forClient).toHaveLength(2);
    expect(forClient.every((r) => r.clientId === 'CLIENT001')).toBe(true);
  });

  it('returns the newest delivery first', () => {
    const feed = new DeliveryFeed();
    feed.record(result({ eventId: 'first' }));
    feed.record(result({ eventId: 'second' }));

    expect(feed.recent(10)[0]?.eventId).toBe('second');
  });

  // A live window, not a ledger: the durable record is in PostgreSQL, so this
  // buffer must never be able to grow without bound.
  it('discards the oldest entries beyond its capacity', () => {
    const feed = new DeliveryFeed(3);
    for (let i = 0; i < 10; i++) {
      feed.record(result({ eventId: `EVT${i}` }));
    }

    const recent = feed.recent(100);
    expect(recent).toHaveLength(3);
    expect(recent.map((r) => r.eventId)).toEqual(['EVT9', 'EVT8', 'EVT7']);
  });

  it('summarises success rate, states and origins', () => {
    const feed = new DeliveryFeed();
    feed.record(result({ status: 'SUCCESS', state: 'DELIVERED', durationMs: 100 }));
    feed.record(result({ status: 'FAILED', state: 'RETRYING', dispatchSource: 'RETRY_SERVICE', durationMs: 300 }));
    feed.record(result({ status: 'SUCCESS', state: 'DELIVERED', dispatchSource: 'SELF_SERVICE', durationMs: 200 }));

    const summary = feed.summary();

    expect(summary.total).toBe(3);
    expect(summary.succeeded).toBe(2);
    expect(summary.failed).toBe(1);
    expect(summary.successRate).toBeCloseTo(2 / 3);
    expect(summary.byState).toEqual({ DELIVERED: 2, RETRYING: 1 });
    expect(summary.bySource).toEqual({ SYSTEM: 1, RETRY_SERVICE: 1, SELF_SERVICE: 1 });
    expect(summary.latency.max).toBe(300);
    expect(summary.clients).toBe(1);
  });

  it('scopes the summary to one client', () => {
    const feed = new DeliveryFeed();
    feed.record(result({ clientId: 'CLIENT001', status: 'SUCCESS' }));
    feed.record(result({ clientId: 'CLIENT002', status: 'FAILED', state: 'RETRYING' }));

    expect(feed.summary('CLIENT002')).toMatchObject({ total: 1, succeeded: 0, failed: 1 });
  });

  it('reports a sane summary before anything has happened', () => {
    const summary = new DeliveryFeed().summary();
    expect(summary.total).toBe(0);
    expect(summary.successRate).toBe(1);
    expect(summary.latency).toEqual({ p50: 0, p95: 0, max: 0 });
  });
});

import { pino } from 'pino';
import request from 'supertest';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import {
  NotificationEvent,
  type EventState,
  type NotificationEventFilters,
  type Page,
} from '../../domain/notification-event.js';
import type {
  DispatchMessage,
  EventPublisher,
  NotificationEventDetail,
  NotificationEventRepository,
} from '../../application/ports.js';
import {
  GetNotificationEventDetail,
  ListNotificationEvents,
} from '../../application/usecase/notification-event-queries.js';
import { ReplayNotificationEvent } from '../../application/usecase/replay-notification-event.js';
import { buildApp } from './app.js';
import { issueDemoToken, type JwtOptions } from './auth.js';

const jwtOptions: JwtOptions = {
  secret: 'a-test-secret-long-enough-to-pass',
  issuer: 'notifications-platform',
  audience: 'notifications-api',
};

const CLIENT_A = 'CLIENT001';
const CLIENT_B = 'CLIENT002';

const ID = {
  deliveredA: '11111111-1111-4111-8111-111111111111',
  failedA: '22222222-2222-4222-8222-222222222222',
  retryingA: '33333333-3333-4333-8333-333333333333',
  failedB: '44444444-4444-4444-8444-444444444444',
};

function event(
  id: string,
  clientId: string,
  state: EventState,
  createdAt: string,
): NotificationEvent {
  return new NotificationEvent({
    id,
    eventId: `EVT-${id.slice(0, 4)}`,
    clientId,
    eventType: 'credit_card_payment',
    payload: { content: 'Credit card payment received for $150.00' },
    state,
    retryCount: state === 'FAILED' ? 5 : 0,
    lastError: state === 'FAILED' ? 'Webhook returned 500' : null,
    createdAt: new Date(createdAt),
    updatedAt: new Date(createdAt),
  });
}

class InMemoryRepository implements NotificationEventRepository {
  events: NotificationEvent[] = [
    event(ID.deliveredA, CLIENT_A, 'DELIVERED', '2024-03-15T09:30:22Z'),
    event(ID.failedA, CLIENT_A, 'FAILED', '2024-03-15T11:20:18Z'),
    event(ID.retryingA, CLIENT_A, 'RETRYING', '2024-03-16T08:00:00Z'),
    event(ID.failedB, CLIENT_B, 'FAILED', '2024-03-15T13:45:10Z'),
  ];

  replayed: string[] = [];

  async findPage(filters: NotificationEventFilters): Promise<Page<NotificationEvent>> {
    const matching = this.events
      .filter((e) => e.belongsTo(filters.clientId))
      .filter((e) => !filters.state || e.state === filters.state)
      .filter((e) => !filters.createdFrom || e.createdAt >= filters.createdFrom)
      .filter((e) => !filters.createdTo || e.createdAt <= filters.createdTo)
      .sort((a, b) => b.createdAt.getTime() - a.createdAt.getTime());

    const offset = (filters.page - 1) * filters.pageSize;
    return {
      items: matching.slice(offset, offset + filters.pageSize),
      page: filters.page,
      pageSize: filters.pageSize,
      total: matching.length,
    };
  }

  async findByIdForClient(id: string, clientId: string): Promise<NotificationEventDetail | null> {
    const found = this.events.find((e) => e.id === id && e.belongsTo(clientId));
    if (!found) return null;
    return {
      event: found,
      attempts: [
        {
          attemptNumber: 1,
          dispatchSource: 'SYSTEM',
          status: found.state === 'DELIVERED' ? 'SUCCESS' : 'FAILED',
          webhookUrl: 'https://client.example.com/hooks',
          requestMethod: 'POST',
          requestPayload: { content: 'x' },
          responseStatus: found.state === 'DELIVERED' ? 200 : 500,
          responseBody: { status: 'received' },
          errorMessage: null,
          durationMs: 42,
          attemptedAt: found.createdAt,
        },
      ],
    };
  }

  async markForReplay(e: NotificationEvent): Promise<void> {
    this.replayed.push(e.id);
  }
}

function buildTestApp(overrides?: {
  repository?: NotificationEventRepository;
  publisher?: EventPublisher;
}) {
  const repository = overrides?.repository ?? new InMemoryRepository();
  const published: DispatchMessage[] = [];
  const publisher =
    overrides?.publisher ??
    ({
      publish: async (m: DispatchMessage) => {
        published.push(m);
      },
    } satisfies EventPublisher);

  const app = buildApp({
    list: new ListNotificationEvents(repository),
    detail: new GetNotificationEventDetail(repository),
    replay: new ReplayNotificationEvent(repository, publisher, {
      now: () => new Date('2026-03-15T09:30:22Z'),
    }),
    jwt: jwtOptions,
    jwtTtlSeconds: 3600,
    enableDemoTokens: true,
    repositoryKind: 'postgres',
    logger: pino({ level: 'silent' }),
  });

  return { app, repository, published };
}

const auth = (clientId: string) => `Bearer ${issueDemoToken(clientId, jwtOptions, 3600).token}`;

describe('self-service API', () => {
  let app: ReturnType<typeof buildTestApp>['app'];
  let repository: InMemoryRepository;
  let published: DispatchMessage[];

  beforeEach(() => {
    const built = buildTestApp();
    app = built.app;
    repository = built.repository as InMemoryRepository;
    published = built.published;
  });

  describe('GET /notification_events', () => {
    it('returns only the caller’s own events', async () => {
      const res = await request(app)
        .get('/notification_events')
        .set('authorization', auth(CLIENT_A))
        .expect(200);

      expect(res.body.notification_events).toHaveLength(3);
      for (const e of res.body.notification_events) {
        expect(e.client_id).toBe(CLIENT_A);
      }
    });

    it('ignores a client_id supplied in the query string', async () => {
      // The old implementation read client_id from here, which made every
      // client's history readable by anyone who could edit a URL.
      await request(app)
        .get('/notification_events')
        .query({ client_id: CLIENT_B })
        .set('authorization', auth(CLIENT_A))
        .expect(400); // strict schema: an unknown parameter is refused outright
    });

    // "allowing to filter by event creation date and delivery_status criteria"
    it('filters by delivery status', async () => {
      const res = await request(app)
        .get('/notification_events')
        .query({ state: 'FAILED' })
        .set('authorization', auth(CLIENT_A))
        .expect(200);

      expect(res.body.notification_events).toHaveLength(1);
      expect(res.body.notification_events[0].notification_event_id).toBe(ID.failedA);
    });

    it('accepts delivery_status as an alias, since that is the brief’s own term', async () => {
      const res = await request(app)
        .get('/notification_events')
        .query({ delivery_status: 'DELIVERED' })
        .set('authorization', auth(CLIENT_A))
        .expect(200);

      expect(res.body.notification_events).toHaveLength(1);
      expect(res.body.notification_events[0].notification_event_id).toBe(ID.deliveredA);
    });

    it('filters by creation date range', async () => {
      const res = await request(app)
        .get('/notification_events')
        .query({
          created_from: '2024-03-16T00:00:00Z',
          created_to: '2024-03-17T00:00:00Z',
        })
        .set('authorization', auth(CLIENT_A))
        .expect(200);

      expect(res.body.notification_events).toHaveLength(1);
      expect(res.body.notification_events[0].notification_event_id).toBe(ID.retryingA);
    });

    it('combines both filters', async () => {
      const res = await request(app)
        .get('/notification_events')
        .query({ state: 'FAILED', created_from: '2024-03-15T00:00:00Z' })
        .set('authorization', auth(CLIENT_A))
        .expect(200);

      expect(res.body.notification_events).toHaveLength(1);
    });

    it('paginates and reports the totals', async () => {
      const res = await request(app)
        .get('/notification_events')
        .query({ page: 1, page_size: 2 })
        .set('authorization', auth(CLIENT_A))
        .expect(200);

      expect(res.body.notification_events).toHaveLength(2);
      expect(res.body.pagination).toMatchObject({ page: 1, page_size: 2, total: 3, total_pages: 2 });
    });

    it('caps the page size so one request cannot pull the whole table', async () => {
      await request(app)
        .get('/notification_events')
        .query({ page_size: 5000 })
        .set('authorization', auth(CLIENT_A))
        .expect(400);
    });

    it('rejects an inverted date range', async () => {
      await request(app)
        .get('/notification_events')
        .query({ created_from: '2024-03-17T00:00:00Z', created_to: '2024-03-15T00:00:00Z' })
        .set('authorization', auth(CLIENT_A))
        .expect(400);
    });
  });

  describe('GET /notification_events/:id', () => {
    it('returns the event with its full delivery history', async () => {
      const res = await request(app)
        .get(`/notification_events/${ID.failedA}`)
        .set('authorization', auth(CLIENT_A))
        .expect(200);

      expect(res.body.notification_event_id).toBe(ID.failedA);
      expect(res.body.event_payload).toEqual({
        content: 'Credit card payment received for $150.00',
      });
      expect(res.body.attempts).toHaveLength(1);
    });

    // The previous mapper read camelCase keys off snake_case rows, so these
    // three fields came back undefined without anything failing.
    it('includes the request fields the old mapper silently dropped', async () => {
      const res = await request(app)
        .get(`/notification_events/${ID.failedA}`)
        .set('authorization', auth(CLIENT_A))
        .expect(200);

      const [attempt] = res.body.attempts;
      expect(attempt.webhook_url).toBe('https://client.example.com/hooks');
      expect(attempt.request_method).toBe('POST');
      expect(attempt.request_payload).toEqual({ content: 'x' });
    });

    it('answers 404 for another client’s event', async () => {
      // 404 rather than 403: a 403 would confirm the id exists.
      await request(app)
        .get(`/notification_events/${ID.failedB}`)
        .set('authorization', auth(CLIENT_A))
        .expect(404);
    });

    it('rejects an id that is not a uuid', async () => {
      await request(app)
        .get('/notification_events/not-a-uuid')
        .set('authorization', auth(CLIENT_A))
        .expect(400);
    });
  });

  describe('POST /notification_events/:id/replay', () => {
    // "Re-send a notification when delivery has definitely failed."
    // The old guard was inverted: it rejected FAILED and accepted PENDING.
    it('replays an event whose delivery definitively failed', async () => {
      const res = await request(app)
        .post(`/notification_events/${ID.failedA}/replay`)
        .set('authorization', auth(CLIENT_A))
        .expect(202);

      expect(res.body).toMatchObject({
        notification_event_id: ID.failedA,
        client_id: CLIENT_A,
        state: 'RETRYING',
      });
      expect(repository.replayed).toEqual([ID.failedA]);
    });

    it('re-enters through the same pipeline, tagged SELF_SERVICE', async () => {
      await request(app)
        .post(`/notification_events/${ID.failedA}/replay`)
        .set('authorization', auth(CLIENT_A))
        .expect(202);

      expect(published).toHaveLength(1);
      expect(published[0]).toMatchObject({
        client_id: CLIENT_A,
        event_type: 'credit_card_payment',
        dispatch_source: 'SELF_SERVICE',
      });
      // The original payload, not a reconstruction of it.
      expect(published[0]?.event_payload).toEqual({
        content: 'Credit card payment received for $150.00',
      });
    });

    it('refuses to replay a delivered event, and says why', async () => {
      const res = await request(app)
        .post(`/notification_events/${ID.deliveredA}/replay`)
        .set('authorization', auth(CLIENT_A))
        .expect(409);

      expect(res.body.error).toBe('event_not_replayable');
      expect(res.body.message).toMatch(/delivered successfully/i);
      expect(published).toHaveLength(0);
    });

    it('refuses to replay an event already queued for an automatic retry', async () => {
      const res = await request(app)
        .post(`/notification_events/${ID.retryingA}/replay`)
        .set('authorization', auth(CLIENT_A))
        .expect(409);

      expect(res.body.message).toMatch(/already scheduled/i);
    });

    it('answers 404 when replaying another client’s event', async () => {
      await request(app)
        .post(`/notification_events/${ID.failedB}/replay`)
        .set('authorization', auth(CLIENT_A))
        .expect(404);

      expect(published).toHaveLength(0);
    });

    it('does not publish when the state change could not be persisted', async () => {
      const repo = new InMemoryRepository();
      repo.markForReplay = vi.fn().mockRejectedValue(new Error('database down'));
      const { app: failing, published: sent } = buildTestApp({ repository: repo });

      await request(failing)
        .post(`/notification_events/${ID.failedA}/replay`)
        .set('authorization', auth(CLIENT_A))
        .expect(500);

      expect(sent).toHaveLength(0);
    });

    it('reports that replay is unavailable on a read-only event source', async () => {
      // The file adapter has no markForReplay at all — its absence is a
      // type-level fact, not a runtime flag.
      const backing = new InMemoryRepository();
      const readOnly: NotificationEventRepository = {
        findPage: (f) => backing.findPage(f),
        findByIdForClient: (id, clientId) => backing.findByIdForClient(id, clientId),
      };

      const { app: fileMode } = buildTestApp({ repository: readOnly });

      await request(fileMode)
        .post(`/notification_events/${ID.failedA}/replay`)
        .set('authorization', auth(CLIENT_A))
        .expect(501);
    });
  });

  describe('authentication', () => {
    it('refuses every endpoint without a token', async () => {
      await request(app).get('/notification_events').expect(401);
      await request(app).get(`/notification_events/${ID.failedA}`).expect(401);
      await request(app).post(`/notification_events/${ID.failedA}/replay`).expect(401);
    });

    it('refuses a token signed with the wrong key', async () => {
      const forged = issueDemoToken(CLIENT_A, { ...jwtOptions, secret: 'attacker-key-000' }, 3600);
      await request(app)
        .get('/notification_events')
        .set('authorization', `Bearer ${forged.token}`)
        .expect(401);
    });
  });
});

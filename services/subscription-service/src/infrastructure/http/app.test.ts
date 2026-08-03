import { randomUUID } from 'node:crypto';

import { pino } from 'pino';
import request from 'supertest';
import { beforeEach, describe, expect, it } from 'vitest';

import { CreateSubscription } from '../../application/usecase/create-subscription.js';
import {
  DeleteSubscription,
  ListSubscriptions,
  ResolveSubscription,
} from '../../application/usecase/subscription-queries.js';
import type { SubscriptionRepository, WebhookUrlValidator } from '../../application/ports.js';
import { WebhookUrlRejected } from '../../application/ports.js';
import type { Subscription } from '../../domain/subscription.js';
import { buildApp } from './app.js';
import { issueDemoToken, type JwtOptions } from './auth.js';

const jwtOptions: JwtOptions = {
  secret: 'a-test-secret-long-enough-to-pass',
  issuer: 'notifications-platform',
  audience: 'notifications-api',
};

class InMemoryRepository implements SubscriptionRepository {
  readonly items: Subscription[] = [];

  async save(subscription: Subscription): Promise<void> {
    const index = this.items.findIndex(
      (s) => s.clientId === subscription.clientId && s.eventType === subscription.eventType,
    );
    if (index >= 0) this.items.splice(index, 1);
    this.items.push(subscription);
  }
  async findActiveByClientAndEventType(clientId: string, eventType: string) {
    return (
      this.items.find(
        (s) => s.clientId === clientId && s.eventType === eventType && s.isActive(),
      ) ?? null
    );
  }
  async findByClient(clientId: string) {
    return this.items.filter((s) => s.clientId === clientId);
  }
  async findById(id: string) {
    return this.items.find((s) => s.id === id) ?? null;
  }
  async delete(id: string): Promise<void> {
    const index = this.items.findIndex((s) => s.id === id);
    if (index >= 0) this.items.splice(index, 1);
  }
}

const permissiveUrls: WebhookUrlValidator = { assertAllowed: async () => undefined };

function buildTestApp(urls: WebhookUrlValidator = permissiveUrls) {
  const repository = new InMemoryRepository();
  const clock = { now: () => new Date('2026-03-15T09:30:22Z') };

  const app = buildApp({
    create: new CreateSubscription(
      repository,
      urls,
      { generate: () => randomUUID().replaceAll('-', '') + randomUUID().replaceAll('-', '') },
      { newId: () => randomUUID() },
      clock,
    ),
    list: new ListSubscriptions(repository),
    remove: new DeleteSubscription(repository, clock),
    resolve: new ResolveSubscription(repository),
    jwt: jwtOptions,
    jwtTtlSeconds: 3600,
    enableDemoTokens: true,
    logger: pino({ level: 'silent' }),
  });

  return { app, repository };
}

function tokenFor(clientId: string): string {
  return issueDemoToken(clientId, jwtOptions, 3600).token;
}

const validBody = {
  webhook_url: 'https://client.example.com/hooks/payments',
  method: 'POST',
  expected_status: 201,
  events: ['credit_card_payment', 'debit_card_withdrawal'],
};

describe('subscription API', () => {
  let app: ReturnType<typeof buildTestApp>['app'];
  let repository: InMemoryRepository;

  beforeEach(() => {
    ({ app, repository } = buildTestApp());
  });

  describe('authentication (OWASP A01, A07)', () => {
    it('rejects a request with no token', async () => {
      await request(app).get('/subscriptions').expect(401);
    });

    it('rejects a token signed with the wrong key', async () => {
      const forged = issueDemoToken('CLIENT001', { ...jwtOptions, secret: 'attacker-key-000' }, 3600);
      await request(app)
        .get('/subscriptions')
        .set('authorization', `Bearer ${forged.token}`)
        .expect(401);
    });

    it('rejects a token minted for another audience', async () => {
      const other = issueDemoToken('CLIENT001', { ...jwtOptions, audience: 'some-other-api' }, 3600);
      await request(app)
        .get('/subscriptions')
        .set('authorization', `Bearer ${other.token}`)
        .expect(401);
    });

    it('does not leak why the token was refused', async () => {
      const res = await request(app)
        .get('/subscriptions')
        .set('authorization', 'Bearer not-a-token')
        .expect(401);
      expect(res.body.message).toBe('Invalid or expired token');
    });
  });

  describe('tenant isolation', () => {
    it('takes the client identity from the token, never from the body', async () => {
      await request(app)
        .post('/subscriptions')
        .set('authorization', `Bearer ${tokenFor('CLIENT001')}`)
        // An attempt to register a webhook against someone else's events.
        .send({ ...validBody, client_id: 'CLIENT002' })
        .expect(400); // strict schema: an unknown field is a rejection

      const withoutTheExtraField = await request(app)
        .post('/subscriptions')
        .set('authorization', `Bearer ${tokenFor('CLIENT001')}`)
        .send(validBody)
        .expect(201);

      for (const sub of withoutTheExtraField.body.subscriptions) {
        expect(sub.client_id).toBe('CLIENT001');
      }
    });

    it('only lists the caller’s own subscriptions', async () => {
      await request(app)
        .post('/subscriptions')
        .set('authorization', `Bearer ${tokenFor('CLIENT001')}`)
        .send(validBody)
        .expect(201);

      const otherClient = await request(app)
        .get('/subscriptions')
        .set('authorization', `Bearer ${tokenFor('CLIENT002')}`)
        .expect(200);

      expect(otherClient.body.subscriptions).toHaveLength(0);
    });

    it('answers 404, not 403, when deleting someone else’s subscription', async () => {
      const created = await request(app)
        .post('/subscriptions')
        .set('authorization', `Bearer ${tokenFor('CLIENT001')}`)
        .send({ ...validBody, events: ['credit_card_payment'] })
        .expect(201);

      const id = created.body.subscriptions[0].subscription_id;

      // 403 would confirm the resource exists, turning the endpoint into an
      // enumeration oracle.
      await request(app)
        .delete(`/subscriptions/${id}`)
        .set('authorization', `Bearer ${tokenFor('CLIENT002')}`)
        .expect(404);

      expect(repository.items).toHaveLength(1);
    });
  });

  describe('creating subscriptions', () => {
    it('creates one subscription per event type and returns the secret once', async () => {
      const res = await request(app)
        .post('/subscriptions')
        .set('authorization', `Bearer ${tokenFor('CLIENT001')}`)
        .send(validBody)
        .expect(201);

      expect(res.body.subscriptions).toHaveLength(2);
      expect(res.body.subscriptions[0].signing_secret).toBeTruthy();
      expect(res.body.subscriptions[0].expected_status).toBe(201);
    });

    it('never returns the secret again afterwards', async () => {
      await request(app)
        .post('/subscriptions')
        .set('authorization', `Bearer ${tokenFor('CLIENT001')}`)
        .send(validBody)
        .expect(201);

      const listed = await request(app)
        .get('/subscriptions')
        .set('authorization', `Bearer ${tokenFor('CLIENT001')}`)
        .expect(200);

      for (const sub of listed.body.subscriptions) {
        expect(sub.signing_secret).toBeUndefined();
        expect(sub.hmac_secret).toBeUndefined();
      }
    });

    it('refuses a duplicate subscription for the same event type', async () => {
      const body = { ...validBody, events: ['credit_card_payment'] };
      const token = `Bearer ${tokenFor('CLIENT001')}`;

      await request(app).post('/subscriptions').set('authorization', token).send(body).expect(201);
      await request(app).post('/subscriptions').set('authorization', token).send(body).expect(409);
    });

    it.each([
      ['no events', { ...validBody, events: [] }],
      ['a bad method', { ...validBody, method: 'DELETE' }],
      ['a nonsense status code', { ...validBody, expected_status: 999 }],
      ['a missing webhook url', { method: 'POST', events: ['x'] }],
    ])('rejects a request with %s', async (_name, body) => {
      await request(app)
        .post('/subscriptions')
        .set('authorization', `Bearer ${tokenFor('CLIENT001')}`)
        .send(body)
        .expect(400);
    });

    it('surfaces an SSRF rejection as a 400 that explains itself', async () => {
      const { app: guarded } = buildTestApp({
        assertAllowed: async () => {
          throw new WebhookUrlRejected('resolves to 169.254.169.254', 'ADDRESS_BLOCKED');
        },
      });

      const res = await request(guarded)
        .post('/subscriptions')
        .set('authorization', `Bearer ${tokenFor('CLIENT001')}`)
        .send(validBody)
        .expect(400);

      expect(res.body.error).toBe('webhook_url_rejected');
      expect(res.body.code).toBe('ADDRESS_BLOCKED');
    });

    it('writes nothing when the webhook url is refused', async () => {
      const { app: guarded, repository: repo } = buildTestApp({
        assertAllowed: async () => {
          throw new WebhookUrlRejected('blocked', 'ADDRESS_BLOCKED');
        },
      });

      await request(guarded)
        .post('/subscriptions')
        .set('authorization', `Bearer ${tokenFor('CLIENT001')}`)
        .send(validBody)
        .expect(400);

      expect(repo.items).toHaveLength(0);
    });
  });

  describe('internal resolution', () => {
    it('returns the signing secret the dispatcher needs', async () => {
      await request(app)
        .post('/subscriptions')
        .set('authorization', `Bearer ${tokenFor('CLIENT001')}`)
        .send({ ...validBody, events: ['credit_card_payment'] })
        .expect(201);

      const res = await request(app)
        .get('/internal/subscriptions/resolve')
        .query({ client_id: 'CLIENT001', event_type: 'credit_card_payment' })
        .expect(200);

      expect(res.body.hmac_secret).toBeTruthy();
      expect(res.body.expected_status).toBe(201);
    });

    it('answers 404 when the client is not subscribed', async () => {
      await request(app)
        .get('/internal/subscriptions/resolve')
        .query({ client_id: 'CLIENT001', event_type: 'nothing_registered' })
        .expect(404);
    });

    it('will not resolve an event type for a client that did not register it', async () => {
      await request(app)
        .post('/subscriptions')
        .set('authorization', `Bearer ${tokenFor('CLIENT001')}`)
        .send({ ...validBody, events: ['credit_card_payment'] })
        .expect(201);

      // The mandatory check: CLIENT002 must not inherit CLIENT001's webhook.
      await request(app)
        .get('/internal/subscriptions/resolve')
        .query({ client_id: 'CLIENT002', event_type: 'credit_card_payment' })
        .expect(404);
    });
  });
});

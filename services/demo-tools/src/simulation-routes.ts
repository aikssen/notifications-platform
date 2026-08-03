import { Router, type Request, type Response } from 'express';

import { deliverAll, publishOne, seed, subscribeAll } from './simulate.js';

/**
 * The simulation actions over HTTP, so the whole demo can be driven from
 * Postman. Switching to a terminal mid-presentation to run a shell script is
 * exactly the kind of seam that makes a demo feel improvised.
 *
 * Everything here stands in for something that is not part of the product: the
 * platform's own producers, and a client's endpoint. It is mounted only in
 * demo-tools, which never runs in a real deployment.
 */
export function simulationRoutes(config: {
  brokers: string[];
  topic: string;
  databaseUrl: string;
  subscriptionsBaseUrl: string;
  defaultWebhookUrl: string;
  receiverUrl: string;
}): Router {
  const router = Router();
  const target = { brokers: config.brokers, topic: config.topic };

  const parse = (req: Request): Record<string, unknown> => {
    if (Buffer.isBuffer(req.body)) {
      const raw = req.body.toString('utf8').trim();
      if (!raw) return {};
      try {
        return JSON.parse(raw) as Record<string, unknown>;
      } catch {
        return {};
      }
    }
    return (req.body as Record<string, unknown>) ?? {};
  };

  const fail = (res: Response, err: unknown): void => {
    const message = err instanceof Error ? err.message : String(err);
    console.error('[simulate] failed:', message);
    res.status(500).json({ error: 'simulation_failed', message });
  };

  /**
   * Registers every (client, event type) pair in the fixture against a webhook.
   *
   * On presentation day, pass the public HTTPS URL the panel provides:
   *   { "webhook_url": "https://their-endpoint/hook" }
   */
  router.post('/simulate/subscribe-all', (req, res) => {
    const body = parse(req);
    const webhookUrl =
      typeof body.webhook_url === 'string' ? body.webhook_url : config.defaultWebhookUrl;
    const expectedStatus =
      typeof body.expected_status === 'number' ? body.expected_status : 200;

    subscribeAll({
      subscriptionsBaseUrl: config.subscriptionsBaseUrl,
      webhookUrl,
      expectedStatus,
      receiverUrl: config.receiverUrl,
    })
      .then((result) => {
        console.log('[simulate] subscribe-all', {
          registered: result.registered,
          existed: result.alreadyExisted,
        });
        res.json({ webhook_url: webhookUrl, ...result });
      })
      .catch((err: unknown) => fail(res, err));
  });

  /** Publishes all ten fixture events — the brief's Task 2. */
  router.post('/simulate/deliver-all', (_req, res) => {
    deliverAll(target)
      .then((messages) => {
        console.log(`[simulate] deliver-all published ${messages.length} events`);
        res.json({
          published: messages.length,
          topic: config.topic,
          events: messages.map((m) => ({
            event_id: m.event_id,
            client_id: m.client_id,
            event_type: m.event_type,
          })),
        });
      })
      .catch((err: unknown) => fail(res, err));
  });

  /**
   * Publishes one event, standing in for a platform microservice.
   *
   * Omit event_id and a fresh one is generated, so repeated demos are not
   * swallowed by the idempotency constraint. Send the same event_id twice to
   * demonstrate the opposite.
   */
  router.post('/simulate/publish', (req, res) => {
    const body = parse(req);
    const clientId = typeof body.client_id === 'string' ? body.client_id : 'CLIENT001';
    const eventType =
      typeof body.event_type === 'string' ? body.event_type : 'credit_card_payment';

    publishOne(target, {
      clientId,
      eventType,
      ...(typeof body.event_id === 'string' ? { eventId: body.event_id } : {}),
      ...(typeof body.content === 'string' ? { content: body.content } : {}),
    })
      .then((message) => {
        console.log('[simulate] published', message.event_id, message.client_id);
        res.json({ published: message, topic: config.topic });
      })
      .catch((err: unknown) => fail(res, err));
  });

  /** Loads the fixture as settled history — an alternative to deliver-all. */
  router.post('/simulate/seed', (_req, res) => {
    if (!config.databaseUrl) {
      res.status(503).json({
        error: 'seed_unavailable',
        message: 'DATABASE_URL is not configured on demo-tools',
      });
      return;
    }

    seed(config.databaseUrl, config.defaultWebhookUrl)
      .then((result) => {
        console.log('[simulate] seeded', result);
        res.json({
          ...result,
          note: 'The FAILED events are the ones the replay endpoint acts on.',
        });
      })
      .catch((err: unknown) => fail(res, err));
  });

  return router;
}

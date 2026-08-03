import { Router, type Response } from 'express';
import { z } from 'zod';

import { DomainError, HTTP_METHODS, type Subscription } from '../../domain/subscription.js';
import { WebhookUrlRejected } from '../../application/ports.js';
import type { CreateSubscription } from '../../application/usecase/create-subscription.js';
import type {
  DeleteSubscription,
  ListSubscriptions,
  ResolveSubscription,
} from '../../application/usecase/subscription-queries.js';
import { clientIdOf, type AuthenticatedRequest } from './auth.js';

/**
 * Every request body is parsed by a schema before it reaches a use case
 * (OWASP A03). Nothing untyped crosses the boundary, and an unknown field is a
 * rejection rather than a silent pass-through.
 */
const createSubscriptionSchema = z
  .object({
    webhook_url: z.string().min(1).max(2048),
    method: z.enum(HTTP_METHODS).default('POST'),
    expected_status: z.number().int().min(100).max(599).default(200),
    events: z.array(z.string().min(1).max(100)).min(1).max(50),
  })
  .strict();

const resolveQuerySchema = z
  .object({
    client_id: z.string().min(1).max(100),
    event_type: z.string().min(1).max(100),
  })
  .strict();

/** Public routes. `client_id` is never accepted from the caller. */
export function subscriptionRoutes(deps: {
  create: CreateSubscription;
  list: ListSubscriptions;
  remove: DeleteSubscription;
}): Router {
  const router = Router();

  router.post('/subscriptions', async (req: AuthenticatedRequest, res: Response) => {
    const parsed = createSubscriptionSchema.safeParse(req.body);
    if (!parsed.success) {
      res.status(400).json({ error: 'invalid_request', details: parsed.error.issues });
      return;
    }

    try {
      const created = await deps.create.execute({
        clientId: clientIdOf(req),
        webhookUrl: parsed.data.webhook_url,
        httpMethod: parsed.data.method,
        expectedStatus: parsed.data.expected_status,
        eventTypes: parsed.data.events,
      });

      res.status(201).json({
        subscriptions: created.map(({ subscription, secret }) => ({
          ...toPublicJson(subscription),
          // The only time this value is ever returned. It is not readable
          // afterwards, so a client that loses it re-subscribes.
          signing_secret: secret,
        })),
        note: 'Store signing_secret now — it is not returned again.',
      });
    } catch (error) {
      respondToError(error, res);
    }
  });

  router.get('/subscriptions', async (req: AuthenticatedRequest, res: Response) => {
    const subscriptions = await deps.list.execute(clientIdOf(req));
    res.json({ subscriptions: subscriptions.map(toPublicJson) });
  });

  router.delete('/subscriptions/:id', async (req: AuthenticatedRequest, res: Response) => {
    try {
      await deps.remove.execute(String(req.params.id), clientIdOf(req));
      res.status(204).send();
    } catch (error) {
      respondToError(error, res);
    }
  });

  return router;
}

/**
 * Internal routes, for the dispatcher.
 *
 * Mounted under /internal and never exposed at the edge: this endpoint returns
 * the signing secret, and it accepts a client_id as a parameter — which is
 * exactly the shape that must never be reachable from the public internet.
 */
export function internalRoutes(deps: { resolve: ResolveSubscription }): Router {
  const router = Router();

  router.get('/internal/subscriptions/resolve', async (req, res) => {
    const parsed = resolveQuerySchema.safeParse(req.query);
    if (!parsed.success) {
      res.status(400).json({ error: 'invalid_request', details: parsed.error.issues });
      return;
    }

    const subscription = await deps.resolve.execute(
      parsed.data.client_id,
      parsed.data.event_type,
    );

    // Not subscribed is a normal answer. The dispatcher distinguishes it from
    // an outage, because the two lead to opposite decisions.
    if (!subscription) {
      res.status(404).json({ error: 'subscription_not_found' });
      return;
    }

    res.json({
      ...toPublicJson(subscription),
      hmac_secret: subscription.hmacSecret,
    });
  });

  return router;
}

/** The public shape. The signing secret is not part of it. */
function toPublicJson(subscription: Subscription) {
  return {
    subscription_id: subscription.id,
    client_id: subscription.clientId,
    event_type: subscription.eventType,
    webhook_url: subscription.webhookUrl,
    http_method: subscription.httpMethod,
    expected_status: subscription.expectedStatus,
    status: subscription.status,
    created_at: subscription.createdAt.toISOString(),
    updated_at: subscription.updatedAt.toISOString(),
  };
}

function respondToError(error: unknown, res: Response): void {
  if (error instanceof WebhookUrlRejected) {
    res.status(400).json({ error: 'webhook_url_rejected', code: error.code, message: error.message });
    return;
  }
  if (error instanceof DomainError) {
    const status = error.code === 'SUBSCRIPTION_NOT_FOUND' ? 404
      : error.code === 'SUBSCRIPTION_ALREADY_EXISTS' ? 409
      : 400;
    res.status(status).json({ error: error.code.toLowerCase(), message: error.message });
    return;
  }
  throw error;
}

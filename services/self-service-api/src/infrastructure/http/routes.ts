import { Router, type Response } from 'express';
import { z } from 'zod';

import {
  DomainError,
  EVENT_STATES,
  type NotificationAttempt,
  type NotificationEvent,
} from '../../domain/notification-event.js';
import type { ListNotificationEvents, GetNotificationEventDetail } from '../../application/usecase/notification-event-queries.js';
import type { ReplayNotificationEvent } from '../../application/usecase/replay-notification-event.js';
import { clientIdOf, type AuthenticatedRequest } from './auth.js';

const MAX_PAGE_SIZE = 100;

/**
 * The list filters the case statement asks for: creation date and delivery
 * status. `delivery_status` is accepted as an alias for `state` because that
 * is the term the brief and the fixture use, and a client should not have to
 * learn our internal vocabulary to filter.
 */
const listQuerySchema = z
  .object({
    state: z.enum(EVENT_STATES).optional(),
    delivery_status: z.enum(EVENT_STATES).optional(),
    created_from: z.iso.datetime({ offset: true }).optional(),
    created_to: z.iso.datetime({ offset: true }).optional(),
    page: z.coerce.number().int().min(1).default(1),
    page_size: z.coerce.number().int().min(1).max(MAX_PAGE_SIZE).default(25),
  })
  .strict();

const eventIdSchema = z.uuid();

export function notificationEventRoutes(deps: {
  list: ListNotificationEvents;
  detail: GetNotificationEventDetail;
  replay: ReplayNotificationEvent;
}): Router {
  const router = Router();

  // GET /notification_events
  router.get('/notification_events', async (req: AuthenticatedRequest, res: Response) => {
    const parsed = listQuerySchema.safeParse(req.query);
    if (!parsed.success) {
      res.status(400).json({ error: 'invalid_request', details: parsed.error.issues });
      return;
    }

    const { state, delivery_status, created_from, created_to, page, page_size } = parsed.data;
    const requestedState = state ?? delivery_status;

    try {
      const result = await deps.list.execute({
        // Never from the query string. This is the whole access control model.
        clientId: clientIdOf(req),
        ...(requestedState ? { state: requestedState } : {}),
        ...(created_from ? { createdFrom: new Date(created_from) } : {}),
        ...(created_to ? { createdTo: new Date(created_to) } : {}),
        page,
        pageSize: page_size,
      });

      res.json({
        notification_events: result.items.map(toSummaryJson),
        pagination: {
          page: result.page,
          page_size: result.pageSize,
          total: result.total,
          total_pages: Math.max(1, Math.ceil(result.total / result.pageSize)),
        },
      });
    } catch (error) {
      respondToError(error, res);
    }
  });

  // GET /notification_events/{notification_event_id}
  router.get(
    '/notification_events/:notification_event_id',
    async (req: AuthenticatedRequest, res: Response) => {
      const id = eventIdSchema.safeParse(req.params.notification_event_id);
      if (!id.success) {
        res.status(400).json({ error: 'invalid_notification_event_id' });
        return;
      }

      const found = await deps.detail.execute(id.data, clientIdOf(req));
      if (!found) {
        res.status(404).json({ error: 'notification_event_not_found' });
        return;
      }

      res.json({
        ...toSummaryJson(found.event),
        event_payload: found.event.payload,
        last_error: found.event.lastError,
        attempts: found.attempts.map(toAttemptJson),
      });
    },
  );

  // POST /notification_events/{notification_event_id}/replay
  router.post(
    '/notification_events/:notification_event_id/replay',
    async (req: AuthenticatedRequest, res: Response) => {
      const id = eventIdSchema.safeParse(req.params.notification_event_id);
      if (!id.success) {
        res.status(400).json({ error: 'invalid_notification_event_id' });
        return;
      }

      try {
        const correlationId = req.id ? String(req.id) : undefined;
        const result = await deps.replay.execute({
          notificationEventId: id.data,
          clientId: clientIdOf(req),
          ...(correlationId ? { correlationId } : {}),
        });

        res.status(202).json({
          notification_event_id: result.notificationEventId,
          client_id: result.clientId,
          state: result.state,
          message: result.message,
        });
      } catch (error) {
        respondToError(error, res);
      }
    },
  );

  return router;
}

function toSummaryJson(event: NotificationEvent) {
  return {
    notification_event_id: event.id,
    event_id: event.eventId,
    client_id: event.clientId,
    event_type: event.eventType,
    // The case statement's vocabulary, alongside our own.
    state: event.state,
    delivery_status: event.state,
    retry_count: event.retryCount,
    created_at: event.createdAt.toISOString(),
    updated_at: event.updatedAt.toISOString(),
  };
}

function toAttemptJson(attempt: NotificationAttempt) {
  return {
    attempt_number: attempt.attemptNumber,
    dispatch_source: attempt.dispatchSource,
    status: attempt.status,
    webhook_url: attempt.webhookUrl,
    request_method: attempt.requestMethod,
    request_payload: attempt.requestPayload,
    response_status: attempt.responseStatus,
    response_body: attempt.responseBody,
    error_message: attempt.errorMessage,
    duration_ms: attempt.durationMs,
    attempted_at: attempt.attemptedAt.toISOString(),
  };
}

function respondToError(error: unknown, res: Response): void {
  if (!(error instanceof DomainError)) throw error;

  const status =
    error.code === 'EVENT_NOT_FOUND' ? 404
    : error.code === 'EVENT_NOT_REPLAYABLE' ? 409
    : error.code === 'REPLAY_NOT_SUPPORTED' ? 501
    : 400;

  res.status(status).json({
    error: error.code.toLowerCase(),
    message: error.message,
  });
}

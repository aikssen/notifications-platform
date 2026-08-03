/**
 * Composition root for the operations monitor.
 */
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

import express from 'express';
import { pino } from 'pino';
import { collectDefaultMetrics, Counter, Registry } from 'prom-client';

import { DeliveryFeed } from './domain/delivery-feed.js';
import { SelfServiceReplayClient } from './infrastructure/http/replay-client.js';
import { SseBroadcaster } from './infrastructure/http/sse.js';
import { KafkaResultStream } from './infrastructure/kafka/result-consumer.js';

const here = dirname(fileURLToPath(import.meta.url));

async function main(): Promise<void> {
  const port = Number(process.env.MONITOR_SERVICE_PORT ?? 3003);
  const brokers = (process.env.KAFKA_BROKERS ?? '')
    .split(',')
    .map((b) => b.trim())
    .filter(Boolean);
  const topic = process.env.KAFKA_TOPIC_RESULT ?? 'notifications.delivery-result';
  const selfServiceUrl = process.env.SELF_SERVICE_BASE_URL ?? 'http://self-service-api:3002';

  const logger = pino({
    level: process.env.LOG_LEVEL ?? 'info',
    base: { service: 'monitor-service' },
  });

  if (brokers.length === 0) throw new Error('missing required configuration: KAFKA_BROKERS');

  const feed = new DeliveryFeed(Number(process.env.MONITOR_BUFFER ?? 500));
  const broadcaster = new SseBroadcaster(logger);
  const replayClient = new SelfServiceReplayClient(selfServiceUrl);

  const registry = new Registry();
  collectDefaultMetrics({ register: registry });
  const observed = new Counter({
    name: 'notifications_monitor_results_total',
    help: 'Delivery results observed by the monitor.',
    labelNames: ['status'],
    registers: [registry],
  });

  // A fresh group per process: this is a live window, so two dashboards should
  // both see everything rather than splitting the stream between them.
  const stream = new KafkaResultStream(
    brokers,
    topic,
    `notifications-monitor-${process.pid}-${Date.now()}`,
    logger,
  );

  await stream.start((result) => {
    feed.record(result);
    broadcaster.broadcast(result);
    observed.inc({ status: result.status });
  });

  const app = express();
  app.disable('x-powered-by');
  app.use(express.json({ limit: '16kb' }));

  app.get('/healthz', (_req, res) => {
    res.json({ status: 'ok', dashboards: broadcaster.clientCount() });
  });

  app.get('/metrics', (_req, res) => {
    res.set('Content-Type', registry.contentType);
    registry
      .metrics()
      .then((body) => res.send(body))
      .catch(() => res.status(500).send('metrics unavailable'));
  });

  app.get('/stream', (req, res) => {
    broadcaster.handle(req, res);
  });

  app.get('/api/summary', (req, res) => {
    const clientId = typeof req.query.client_id === 'string' ? req.query.client_id : undefined;
    res.json(feed.summary(clientId));
  });

  app.get('/api/deliveries', (req, res) => {
    const clientId = typeof req.query.client_id === 'string' ? req.query.client_id : undefined;
    const limit = Math.min(Number(req.query.limit ?? 100), 500);
    res.json({ deliveries: feed.recent(limit, clientId) });
  });

  app.post('/api/replay', (req, res) => {
    const { notification_event_id: id, client_id: clientId } = req.body ?? {};
    if (typeof id !== 'string' || typeof clientId !== 'string') {
      res.status(400).json({ error: 'notification_event_id and client_id are required' });
      return;
    }

    replayClient
      .replay(id, clientId)
      .then((outcome) => {
        logger.info({ id, clientId, status: outcome.status }, 'operator requested a replay');
        res.status(outcome.ok ? 202 : outcome.status).json(outcome.body);
      })
      .catch((err: unknown) => {
        logger.error({ err }, 'replay request failed');
        res.status(502).json({ error: 'replay_unavailable' });
      });
  });

  app.use(express.static(join(here, 'public')));

  const server = app.listen(port, () => {
    logger.info({ port }, 'operations dashboard listening');
  });

  const shutdown = (signal: string): void => {
    logger.info({ signal }, 'shutting down');
    server.close(() => {
      stream
        .stop()
        .then(() => process.exit(0))
        .catch(() => process.exit(1));
    });
    setTimeout(() => process.exit(1), 10_000).unref();
  };

  process.on('SIGTERM', () => shutdown('SIGTERM'));
  process.on('SIGINT', () => shutdown('SIGINT'));
}

main().catch((err: unknown) => {
  // eslint-disable-next-line no-console -- the logger may not exist yet
  console.error('monitor service failed to start', err);
  process.exit(1);
});

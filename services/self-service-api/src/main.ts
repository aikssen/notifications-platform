/**
 * Composition root.
 *
 * The interesting part is the repository branch: the same three use cases are
 * built either way, and neither of them can tell which adapter it received.
 */
import { resolve } from 'node:path';

import { Kafka, type Producer } from 'kafkajs';
import pg from 'pg';
import { pino } from 'pino';

import {
  GetNotificationEventDetail,
  ListNotificationEvents,
} from './application/usecase/notification-event-queries.js';
import { ReplayNotificationEvent } from './application/usecase/replay-notification-event.js';
import type { EventPublisher, NotificationEventRepository } from './application/ports.js';
import { loadConfig } from './infrastructure/config.js';
import { PostgresNotificationEventRepository } from './infrastructure/db/postgres-event-repository.js';
import { FileNotificationEventRepository } from './infrastructure/fixture/file-event-repository.js';
import { buildApp } from './infrastructure/http/app.js';
import { KafkaEventPublisher } from './infrastructure/kafka/event-publisher.js';

async function main(): Promise<void> {
  const config = loadConfig();
  const logger = pino({ level: config.logLevel, base: { service: 'self-service-api' } });

  logger.info(
    {
      port: config.port,
      repository: config.eventsRepository,
      demoTokens: config.enableDemoTokens,
    },
    'starting',
  );

  const closers: Array<() => Promise<void>> = [];

  // --- the repository port, one of two adapters ------------------------
  let repository: NotificationEventRepository;

  if (config.eventsRepository === 'file') {
    repository = new FileNotificationEventRepository(
      resolve(process.cwd(), config.fixturePath),
      config.fixtureWebhookUrl,
    );
    logger.warn(
      { fixture: config.fixturePath },
      'reading events from the JSON fixture — read-only, replay is unavailable',
    );
  } else {
    const pool = new pg.Pool({ connectionString: config.databaseUrl, max: 10 });
    await pool.query('SELECT 1');
    logger.info('database connected');
    repository = new PostgresNotificationEventRepository(pool);
    closers.push(() => pool.end());
  }

  // --- the publisher port ---------------------------------------------
  let publisher: EventPublisher = {
    publish: async () => {
      throw new Error('no Kafka brokers configured, replay is unavailable');
    },
  };

  if (config.kafkaBrokers.length > 0) {
    const kafka = new Kafka({ clientId: 'self-service-api', brokers: config.kafkaBrokers });
    const producer: Producer = kafka.producer();
    await producer.connect();
    logger.info('kafka producer connected');
    publisher = new KafkaEventPublisher(producer, config.kafkaTopicDispatch);
    closers.push(() => producer.disconnect());
  }

  const clock = { now: () => new Date() };

  const app = buildApp({
    list: new ListNotificationEvents(repository),
    detail: new GetNotificationEventDetail(repository),
    replay: new ReplayNotificationEvent(repository, publisher, clock),
    jwt: config.jwt,
    jwtTtlSeconds: config.jwtTtlSeconds,
    enableDemoTokens: config.enableDemoTokens,
    repositoryKind: config.eventsRepository,
    logger,
  });

  const server = app.listen(config.port, () => {
    logger.info({ port: config.port }, 'self-service api listening');
  });

  const shutdown = (signal: string): void => {
    logger.info({ signal }, 'shutting down');
    server.close(() => {
      Promise.allSettled(closers.map((close) => close()))
        .then(() => {
          logger.info('stopped');
          process.exit(0);
        })
        .catch(() => process.exit(1));
    });
    setTimeout(() => {
      logger.warn('shutdown grace period expired, exiting anyway');
      process.exit(1);
    }, 15_000).unref();
  };

  process.on('SIGTERM', () => shutdown('SIGTERM'));
  process.on('SIGINT', () => shutdown('SIGINT'));
}

main().catch((err: unknown) => {
  // eslint-disable-next-line no-console -- the logger may not exist yet
  console.error('self-service api failed to start', err);
  process.exit(1);
});

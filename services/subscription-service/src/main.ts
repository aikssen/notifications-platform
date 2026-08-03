/**
 * Composition root. The only file in this service that names a concrete
 * adapter — read it top to bottom and you know exactly what is wired to what.
 * No container, no decorators, no reflection.
 */
import { randomBytes, randomUUID } from 'node:crypto';

import pg from 'pg';
import { pino } from 'pino';

import { CreateSubscription } from './application/usecase/create-subscription.js';
import {
  DeleteSubscription,
  ListSubscriptions,
  ResolveSubscription,
} from './application/usecase/subscription-queries.js';
import { loadConfig } from './infrastructure/config.js';
import { PostgresSubscriptionRepository } from './infrastructure/db/subscription-repository.js';
import { buildApp } from './infrastructure/http/app.js';
import { WebhookUrlGuard } from './infrastructure/security/webhook-url-guard.js';

async function main(): Promise<void> {
  const config = loadConfig();
  const logger = pino({ level: config.logLevel, base: { service: 'subscription-service' } });

  // Note what is logged and what is not: never the database URL, which carries
  // a password, and never the JWT secret.
  logger.info(
    {
      port: config.port,
      webhookRequireHttps: config.webhookRequireHttps,
      webhookAllowPrivateNetworks: config.webhookAllowPrivateNetworks,
      demoTokens: config.enableDemoTokens,
    },
    'starting',
  );

  if (config.webhookAllowPrivateNetworks || !config.webhookRequireHttps) {
    logger.warn('SSRF protections are relaxed — acceptable for a local demo, never for production');
  }

  const pool = new pg.Pool({ connectionString: config.databaseUrl, max: 10 });
  await pool.query('SELECT 1');
  logger.info('database connected');

  // --- driven adapters -----------------------------------------------
  const repository = new PostgresSubscriptionRepository(pool);
  const webhookUrls = new WebhookUrlGuard({
    requireHttps: config.webhookRequireHttps,
    allowPrivateNetworks: config.webhookAllowPrivateNetworks,
  });
  const secrets = { generate: () => randomBytes(32).toString('hex') };
  const ids = { newId: () => randomUUID() };
  const clock = { now: () => new Date() };

  // --- use cases ------------------------------------------------------
  const app = buildApp({
    create: new CreateSubscription(repository, webhookUrls, secrets, ids, clock),
    list: new ListSubscriptions(repository),
    remove: new DeleteSubscription(repository, clock),
    resolve: new ResolveSubscription(repository),
    jwt: config.jwt,
    jwtTtlSeconds: config.jwtTtlSeconds,
    enableDemoTokens: config.enableDemoTokens,
    logger,
  });

  const server = app.listen(config.port, () => {
    logger.info({ port: config.port }, 'subscription service listening');
  });

  // Graceful shutdown: stop accepting connections, let in-flight requests
  // finish, then release the pool. Without this an orchestrator's rolling
  // deploy drops requests that were already being served.
  const shutdown = (signal: string): void => {
    logger.info({ signal }, 'shutting down');
    server.close(() => {
      pool
        .end()
        .then(() => {
          logger.info('stopped');
          process.exit(0);
        })
        .catch((err: unknown) => {
          logger.error({ err }, 'pool did not close cleanly');
          process.exit(1);
        });
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
  console.error('subscription service failed to start', err);
  process.exit(1);
});

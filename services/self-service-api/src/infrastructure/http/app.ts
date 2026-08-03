import express, { type Express, type NextFunction, type Request, type Response } from 'express';
import rateLimit from 'express-rate-limit';
import helmet from 'helmet';
import { pinoHttp } from 'pino-http';
import type { Logger } from 'pino';
import { collectDefaultMetrics, Registry } from 'prom-client';

import type {
  GetNotificationEventDetail,
  ListNotificationEvents,
} from '../../application/usecase/notification-event-queries.js';
import type { ReplayNotificationEvent } from '../../application/usecase/replay-notification-event.js';
import { authenticate, issueDemoToken, type JwtOptions } from './auth.js';
import { notificationEventRoutes } from './routes.js';

export interface AppDeps {
  list: ListNotificationEvents;
  detail: GetNotificationEventDetail;
  replay: ReplayNotificationEvent;
  jwt: JwtOptions;
  jwtTtlSeconds: number;
  enableDemoTokens: boolean;
  repositoryKind: 'postgres' | 'file';
  logger: Logger;
  registry?: Registry;
}

export function buildApp(deps: AppDeps): Express {
  const app = express();

  app.set('trust proxy', 1);
  app.disable('x-powered-by');

  app.use(helmet());
  app.use(express.json({ limit: '64kb' }));
  app.use(
    pinoHttp({
      logger: deps.logger,
      // Operational endpoints are polled constantly — Prometheus every few
      // seconds, the container healthcheck as often. Logging them buries the
      // delivery traffic that actually says what the platform is doing.
      autoLogging: {
        ignore: (req) => req.url === '/healthz' || req.url === '/readyz' || req.url === '/metrics',
      },
      // Log what identifies a request, not its entire header set. The default
      // serializers dump every helmet response header on every line, which is
      // roughly 1.5 KB of noise per request.
      serializers: {
        req: (req) => ({ method: req.method, url: req.url }),
        res: (res) => ({ statusCode: res.statusCode }),
      },
      genReqId: (req, res) => {
        const existing = req.headers['x-correlation-id'];
        const id = typeof existing === 'string' && existing ? existing : crypto.randomUUID();
        res.setHeader('x-correlation-id', id);
        return id;
      },
      // A bearer token in a log line is a credential in a log line.
      redact: ['req.headers.authorization'],
    }),
  );

  const registry = deps.registry ?? new Registry();
  collectDefaultMetrics({ register: registry });

  app.get('/healthz', (_req, res) => {
    res.json({ status: 'ok', repository: deps.repositoryKind });
  });

  app.get('/metrics', (_req, res) => {
    res.set('Content-Type', registry.contentType);
    registry
      .metrics()
      .then((body) => res.send(body))
      .catch(() => res.status(500).send('metrics unavailable'));
  });

  if (deps.enableDemoTokens) {
    app.post('/auth/token', (req: Request, res: Response) => {
      const clientId = typeof req.body?.client_id === 'string' ? req.body.client_id : '';
      if (!clientId) {
        res.status(400).json({ error: 'invalid_request', message: 'client_id is required' });
        return;
      }
      const { token, expiresIn } = issueDemoToken(clientId, deps.jwt, deps.jwtTtlSeconds);
      res.json({ access_token: token, token_type: 'Bearer', expires_in: expiresIn });
    });
  }

  app.use(
    rateLimit({
      windowMs: 60_000,
      limit: 120,
      standardHeaders: 'draft-7',
      legacyHeaders: false,
      message: { error: 'rate_limited' },
    }),
  );

  // Everything past this line requires a verified token, and every handler
  // takes the client identity from it.
  app.use(authenticate(deps.jwt));
  app.use(
    notificationEventRoutes({ list: deps.list, detail: deps.detail, replay: deps.replay }),
  );

  app.use((_req, res) => {
    res.status(404).json({ error: 'not_found' });
  });

  app.use((err: Error, req: Request, res: Response, _next: NextFunction) => {
    deps.logger.error({ err, url: req.originalUrl }, 'unhandled request error');
    res.status(500).json({ error: 'internal_error' });
  });

  return app;
}

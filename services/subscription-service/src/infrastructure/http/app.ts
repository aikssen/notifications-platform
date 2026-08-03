import express, { type Express, type NextFunction, type Request, type Response } from 'express';
import rateLimit from 'express-rate-limit';
import helmet from 'helmet';
import { pinoHttp } from 'pino-http';
import type { Logger } from 'pino';
import { collectDefaultMetrics, Registry } from 'prom-client';

import type { CreateSubscription } from '../../application/usecase/create-subscription.js';
import type {
  DeleteSubscription,
  ListSubscriptions,
  ResolveSubscription,
} from '../../application/usecase/subscription-queries.js';
import { authenticate, issueDemoToken, type JwtOptions } from './auth.js';
import { internalRoutes, subscriptionRoutes } from './routes.js';

export interface AppDeps {
  create: CreateSubscription;
  list: ListSubscriptions;
  remove: DeleteSubscription;
  resolve: ResolveSubscription;
  jwt: JwtOptions;
  jwtTtlSeconds: number;
  enableDemoTokens: boolean;
  logger: Logger;
  registry?: Registry;
}

export function buildApp(deps: AppDeps): Express {
  const app = express();

  // Behind a load balancer, so rate limiting and logging see the real client
  // address rather than the proxy's.
  app.set('trust proxy', 1);
  app.disable('x-powered-by');

  app.use(helmet());
  app.use(express.json({ limit: '64kb' }));
  app.use(
    pinoHttp({
      logger: deps.logger,
      // A correlation id that follows the request all the way to the webhook
      // call, so one client complaint can be traced across services.
      genReqId: (req, res) => {
        const existing = req.headers['x-correlation-id'];
        const id = typeof existing === 'string' && existing ? existing : crypto.randomUUID();
        res.setHeader('x-correlation-id', id);
        return id;
      },
      redact: ['req.headers.authorization', 'res.headers["set-cookie"]'],
    }),
  );

  const registry = deps.registry ?? new Registry();
  collectDefaultMetrics({ register: registry });

  app.get('/healthz', (_req, res) => {
    res.json({ status: 'ok' });
  });

  app.get('/metrics', (_req, res) => {
    res.set('Content-Type', registry.contentType);
    registry
      .metrics()
      .then((body) => res.send(body))
      .catch(() => res.status(500).send('metrics unavailable'));
  });

  // Internal first, and without the public authenticate middleware: this route
  // is reachable only from the private network, and it is what the dispatcher
  // calls before every delivery.
  app.use(internalRoutes({ resolve: deps.resolve }));

  if (deps.enableDemoTokens) {
    // Demo affordance, behind an explicit flag. A real deployment gets tokens
    // from the platform's identity provider and never mounts this.
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

  app.use(authenticate(deps.jwt));
  app.use(subscriptionRoutes({ create: deps.create, list: deps.list, remove: deps.remove }));

  app.use((_req, res) => {
    res.status(404).json({ error: 'not_found' });
  });

  // Errors are logged in full and reported in outline. A stack trace in an API
  // response is a map of the internals (OWASP A05).
  app.use((err: Error, req: Request, res: Response, _next: NextFunction) => {
    deps.logger.error({ err, url: req.originalUrl }, 'unhandled request error');
    res.status(500).json({ error: 'internal_error' });
  });

  return app;
}

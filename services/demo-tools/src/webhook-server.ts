/**
 * Stands in for a client's webhook endpoint.
 *
 * The previous mock failed on a coin flip — 50% of deliveries returned 500 —
 * which made the retry path impossible to demonstrate reliably and, worse,
 * made the happy path unreliable too. The old runbook literally said "if it
 * fails four times, publish another event and try again". That is not
 * something to do in front of a panel.
 *
 * Here failure is asked for explicitly:
 *
 *   POST /control  {"failNext": 3}          the next 3 deliveries return 500
 *   POST /control  {"status": 503}          every delivery returns 503
 *   POST /control  {"delayMs": 6000}        every delivery hangs, to show timeouts
 *   POST /control  {"reset": true}          back to succeeding
 *
 * It also verifies the HMAC signature on every delivery, which doubles as the
 * reference implementation a real client would write on their side.
 */
import { createHmac, timingSafeEqual } from 'node:crypto';

import express from 'express';

import { simulationRoutes } from './simulation-routes.js';
import { closeProducer } from './simulate.js';

const app = express();
const PORT = Number(process.env.DEMO_TOOLS_PORT ?? 3004);

// A fallback secret for the single-subscription case.
const DEFAULT_SECRET = process.env.WEBHOOK_SIGNING_SECRET ?? '';

/**
 * Secrets by `client_id::event_type`, which is how a real client would store
 * them: the subscription service issues one per subscription, and the
 * delivery carries X-Client-Id and X-Event-Type so the receiver knows which to
 * use. `subscribe-all` pushes them here after registering.
 */
const secrets = new Map<string, string>();

function secretFor(clientId: string | undefined, eventType: string | undefined): string {
  return secrets.get(`${clientId ?? ''}::${eventType ?? ''}`) ?? DEFAULT_SECRET;
}

// How far out of date a signature may be. Binding a timestamp into the signed
// material is what stops a captured request from being replayed forever.
const MAX_SIGNATURE_AGE_SECONDS = 300;

interface Behaviour {
  failNext: number;
  status: number;
  delayMs: number;
}

const behaviour: Behaviour = { failNext: 0, status: 200, delayMs: 0 };

interface Received {
  receivedAt: string;
  notificationEventId: string | undefined;
  eventId: string | undefined;
  clientId: string | undefined;
  eventType: string | undefined;
  dispatchSource: string | undefined;
  signature: 'valid' | 'invalid' | 'stale' | 'missing' | 'unverified';
  respondedWith: number;
}

// A bounded ring buffer, so a load test cannot exhaust memory.
const MAX_HISTORY = 500;
const history: Received[] = [];

// Raw body, because a signature is over bytes. Re-serialising parsed JSON
// changes key order and whitespace, and the signature stops matching.
app.use(express.raw({ type: '*/*', limit: '1mb' }));

// The simulation endpoints, so the whole demo can be driven from Postman
// without switching to a terminal.
app.use(
  simulationRoutes({
    brokers: (process.env.KAFKA_BROKERS ?? 'kafka:29092').split(',').map((b) => b.trim()).filter(Boolean),
    topic: process.env.KAFKA_TOPIC_DISPATCH ?? 'notifications.dispatch',
    databaseUrl: process.env.DATABASE_URL ?? '',
    subscriptionsBaseUrl: process.env.SUBSCRIPTIONS_BASE_URL ?? 'http://subscription-service:3001',
    defaultWebhookUrl: process.env.WEBHOOK_URL ?? `http://demo-tools:${PORT}/webhook`,
    receiverUrl: `http://localhost:${PORT}`,
  }),
);

app.get('/healthz', (_req, res) => {
  res.json({ status: 'ok', behaviour, received: history.length, secrets: secrets.size });
});

/**
 * Registers the signing secrets the subscription service issued, so deliveries
 * are actually verified instead of merely reported as signed.
 */
app.post('/secrets', (req, res) => {
  const body = parseJson(req.body) as {
    secrets?: Array<{ client_id: string; event_type: string; secret: string }>;
  };

  for (const entry of body.secrets ?? []) {
    secrets.set(`${entry.client_id}::${entry.event_type}`, entry.secret);
  }
  console.log(`[secrets] holding ${secrets.size} subscription secrets`);
  res.json({ registered: secrets.size });
});

app.post('/control', (req, res) => {
  const body = parseJson(req.body) as Partial<Behaviour> & { reset?: boolean };

  if (body.reset) {
    behaviour.failNext = 0;
    behaviour.status = 200;
    behaviour.delayMs = 0;
  }
  if (typeof body.failNext === 'number') behaviour.failNext = body.failNext;
  if (typeof body.status === 'number') behaviour.status = body.status;
  if (typeof body.delayMs === 'number') behaviour.delayMs = body.delayMs;

  console.log('[control]', behaviour);
  res.json(behaviour);
});

app.get('/received', (req, res) => {
  const clientId = typeof req.query.client_id === 'string' ? req.query.client_id : undefined;
  const items = clientId ? history.filter((r) => r.clientId === clientId) : history;
  res.json({ total: items.length, received: items });
});

app.post('/received/reset', (_req, res) => {
  history.length = 0;
  res.status(204).send();
});

app.post('/webhook', async (req, res) => {
  const raw: Buffer = Buffer.isBuffer(req.body) ? req.body : Buffer.alloc(0);
  const payload = parseJson(raw) as Record<string, unknown>;

  const signature = verifySignature(
    raw,
    req.header('x-signature'),
    req.header('x-timestamp'),
    secretFor(req.header('x-client-id'), req.header('x-event-type')),
  );

  // Query parameters override the stored behaviour for a single call, which is
  // handy when driving the demo from curl.
  const oneOffStatus = Number(req.query.status);
  const oneOffDelay = Number(req.query.delay);

  const delay = Number.isFinite(oneOffDelay) ? oneOffDelay : behaviour.delayMs;
  if (delay > 0) await sleep(delay);

  let status = behaviour.status;
  if (behaviour.failNext > 0) {
    behaviour.failNext -= 1;
    status = 500;
  }
  if (Number.isFinite(oneOffStatus) && oneOffStatus >= 100) {
    status = oneOffStatus;
  }

  const record: Received = {
    receivedAt: new Date().toISOString(),
    notificationEventId: req.header('x-notification-event-id'),
    eventId: req.header('x-event-id'),
    clientId: req.header('x-client-id'),
    eventType: req.header('x-event-type'),
    dispatchSource: req.header('x-dispatch-source'),
    signature,
    respondedWith: status,
  };

  history.push(record);
  if (history.length > MAX_HISTORY) history.shift();

  console.log(
    `[webhook] ${record.eventType ?? '?'} ${record.clientId ?? '?'} ` +
      `source=${record.dispatchSource ?? '?'} signature=${signature} -> ${status}`,
    payload,
  );

  if (status >= 400) {
    res.status(status).json({ error: 'simulated_failure', status });
    return;
  }
  res.status(status).json({ status: 'received' });
});

const server = app.listen(PORT, () => {
  console.log(`demo-tools listening on ${PORT}`);
  console.log('  POST /simulate/subscribe-all   register the fixture\'s subscriptions');
  console.log('  POST /simulate/deliver-all     push all ten fixture events');
  console.log('  POST /simulate/publish         publish one event');
  console.log('  POST /simulate/seed            load the fixture as history');
  console.log('  POST /control                  make the webhook fail on demand');
  console.log('  GET  /received                 what the client endpoint got');
});

const shutdown = (signal: string): void => {
  console.log(`[demo-tools] ${signal}, shutting down`);
  server.close(() => {
    closeProducer()
      .then(() => process.exit(0))
      .catch(() => process.exit(1));
  });
  setTimeout(() => process.exit(1), 10_000).unref();
};

process.on('SIGTERM', () => shutdown('SIGTERM'));
process.on('SIGINT', () => shutdown('SIGINT'));

/**
 * The reference implementation of signature verification, for a client to
 * copy. The signed material is `<unix timestamp>.<raw body>`, so a captured
 * request stops being replayable once it ages out.
 */
function verifySignature(
  body: Buffer,
  signature: string | undefined,
  timestamp: string | undefined,
  secret: string,
): Received['signature'] {
  if (!signature || !timestamp) return 'missing';
  if (!secret) return 'unverified';

  const age = Math.abs(Math.floor(Date.now() / 1000) - Number(timestamp));
  if (!Number.isFinite(age) || age > MAX_SIGNATURE_AGE_SECONDS) return 'stale';

  const expected =
    'sha256=' +
    createHmac('sha256', secret)
      .update(timestamp)
      .update('.')
      .update(body)
      .digest('hex');

  const a = Buffer.from(signature);
  const b = Buffer.from(expected);

  // Constant-time comparison: a plain === leaks how much of the signature was
  // correct through timing.
  return a.length === b.length && timingSafeEqual(a, b) ? 'valid' : 'invalid';
}

function parseJson(raw: unknown): unknown {
  if (!Buffer.isBuffer(raw) || raw.length === 0) return {};
  try {
    return JSON.parse(raw.toString('utf8'));
  } catch {
    return { raw: raw.toString('utf8').slice(0, 200) };
  }
}

function sleep(ms: number): Promise<void> {
  return new Promise((r) => setTimeout(r, ms));
}

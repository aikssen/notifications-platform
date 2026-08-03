export interface Config {
  port: number;
  databaseUrl: string;
  kafkaBrokers: string[];
  kafkaTopicDispatch: string;
  jwt: { secret: string; issuer: string; audience: string };
  jwtTtlSeconds: number;
  enableDemoTokens: boolean;
  /**
   * Which adapter backs the repository port. Switching this to `file` makes
   * every endpoint read from the case statement's JSON fixture instead of
   * PostgreSQL, with no other change anywhere in the service.
   */
  eventsRepository: 'postgres' | 'file';
  fixturePath: string;
  fixtureWebhookUrl: string;
  logLevel: string;
}

export function loadConfig(env: NodeJS.ProcessEnv = process.env): Config {
  const eventsRepository = (env.EVENTS_REPOSITORY ?? 'postgres') as Config['eventsRepository'];
  if (eventsRepository !== 'postgres' && eventsRepository !== 'file') {
    throw new Error(`EVENTS_REPOSITORY must be "postgres" or "file", got "${eventsRepository}"`);
  }

  const secret = required(env, 'JWT_SECRET');
  if (secret.length < 16) {
    throw new Error('JWT_SECRET is too short to be a signing key');
  }

  return {
    port: int(env.SELF_SERVICE_PORT, 3002),
    // Not required in file mode: there is no database to reach.
    databaseUrl: eventsRepository === 'postgres' ? required(env, 'DATABASE_URL') : '',
    kafkaBrokers: (env.KAFKA_BROKERS ?? '')
      .split(',')
      .map((b) => b.trim())
      .filter(Boolean),
    kafkaTopicDispatch: env.KAFKA_TOPIC_DISPATCH ?? 'notifications.dispatch',
    jwt: {
      secret,
      issuer: env.JWT_ISSUER ?? 'notifications-platform',
      audience: env.JWT_AUDIENCE ?? 'notifications-api',
    },
    jwtTtlSeconds: int(env.JWT_TTL_SECONDS, 3600),
    enableDemoTokens: bool(env.ENABLE_DEMO_TOKENS, false),
    eventsRepository,
    fixturePath: env.FIXTURE_PATH ?? '../../fixtures/notification_events.json',
    fixtureWebhookUrl: env.FIXTURE_WEBHOOK_URL ?? 'https://client.example.com/webhooks',
    logLevel: env.LOG_LEVEL ?? 'info',
  };
}

function required(env: NodeJS.ProcessEnv, key: string): string {
  const value = env[key];
  if (!value) throw new Error(`missing required configuration: ${key}`);
  return value;
}

function int(raw: string | undefined, fallback: number): number {
  if (!raw) return fallback;
  const value = Number(raw);
  if (!Number.isInteger(value)) throw new Error(`expected an integer, got ${raw}`);
  return value;
}

function bool(raw: string | undefined, fallback: boolean): boolean {
  if (raw === undefined || raw === '') return fallback;
  return ['1', 'true', 'yes', 'on'].includes(raw.toLowerCase());
}

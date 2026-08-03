export interface Config {
  port: number;
  databaseUrl: string;
  jwt: { secret: string; issuer: string; audience: string };
  jwtTtlSeconds: number;
  enableDemoTokens: boolean;
  webhookRequireHttps: boolean;
  webhookAllowPrivateNetworks: boolean;
  logLevel: string;
}

export function loadConfig(env: NodeJS.ProcessEnv = process.env): Config {
  const databaseUrl = required(env, 'DATABASE_URL');
  const secret = required(env, 'JWT_SECRET');

  if (secret.length < 16) {
    throw new Error('JWT_SECRET is too short to be a signing key');
  }

  return {
    port: int(env.SUBSCRIPTION_SERVICE_PORT, 3001),
    databaseUrl,
    jwt: {
      secret,
      issuer: env.JWT_ISSUER ?? 'notifications-platform',
      audience: env.JWT_AUDIENCE ?? 'notifications-api',
    },
    jwtTtlSeconds: int(env.JWT_TTL_SECONDS, 3600),
    // Off unless explicitly enabled, so no deployment mints its own tokens by
    // accident.
    enableDemoTokens: bool(env.ENABLE_DEMO_TOKENS, false),
    webhookRequireHttps: bool(env.WEBHOOK_REQUIRE_HTTPS, true),
    webhookAllowPrivateNetworks: bool(env.WEBHOOK_ALLOW_PRIVATE_NETWORKS, false),
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

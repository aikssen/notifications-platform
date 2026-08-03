/**
 * Registers a subscription for every (client, event type) pair in the fixture,
 * pointing at whichever webhook URL the demo is targeting.
 *
 * It goes through the public API rather than writing to the database, because
 * that is the path the case statement's first requirement lives on: the
 * subscription service validates the URL, refuses anything that resolves
 * somewhere it should not, and hands back a signing secret.
 *
 * On presentation day, point WEBHOOK_URL at the public HTTPS endpoint the
 * panel provides and run this before `deliver-all`.
 */
import { env, loadFixture } from './fixture.js';

interface TokenResponse {
  access_token: string;
}

async function tokenFor(baseUrl: string, clientId: string): Promise<string> {
  const res = await fetch(`${baseUrl}/auth/token`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ client_id: clientId }),
  });
  if (!res.ok) {
    throw new Error(
      `could not obtain a token for ${clientId}: ${res.status}. ` +
        'Is ENABLE_DEMO_TOKENS set on the subscription service?',
    );
  }
  return ((await res.json()) as TokenResponse).access_token;
}

async function main(): Promise<void> {
  const baseUrl = env('SUBSCRIPTIONS_BASE_URL', 'http://localhost:3001');
  const webhookUrl = env('WEBHOOK_URL', 'http://localhost:3004/webhook');
  const expectedStatus = Number(process.env.EXPECTED_STATUS ?? 200);

  const events = await loadFixture();

  // One subscription row per client and event type — the pair the dispatcher
  // resolves on.
  const pairs = new Map<string, Set<string>>();
  for (const event of events) {
    const forClient = pairs.get(event.client_id) ?? new Set<string>();
    forClient.add(event.event_type);
    pairs.set(event.client_id, forClient);
  }

  const registered: Array<{ client_id: string; event_type: string; secret: string }> = [];

  console.log(`registering subscriptions against ${webhookUrl}\n`);

  for (const [clientId, eventTypes] of pairs) {
    const token = await tokenFor(baseUrl, clientId);

    const res = await fetch(`${baseUrl}/subscriptions`, {
      method: 'POST',
      headers: {
        'content-type': 'application/json',
        authorization: `Bearer ${token}`,
      },
      body: JSON.stringify({
        webhook_url: webhookUrl,
        method: 'POST',
        expected_status: expectedStatus,
        events: [...eventTypes],
      }),
    });

    const body = (await res.json()) as Record<string, unknown>;

    if (res.status === 409) {
      console.log(`${clientId}  already subscribed to ${eventTypes.size} event types`);
      continue;
    }
    if (!res.ok) {
      console.error(`${clientId}  failed: ${res.status}`, body);
      process.exitCode = 1;
      continue;
    }

    console.log(`${clientId}  subscribed to ${eventTypes.size} event types`);
    for (const type of eventTypes) console.log(`            ${type}`);

    // Hand the signing secrets to the receiver, exactly as a real client would
    // store them after registering. Without this it can report that a delivery
    // was signed, but not that the signature is correct.
    const issued = (body.subscriptions ?? []) as Array<{
      client_id: string;
      event_type: string;
      signing_secret: string;
    }>;
    registered.push(
      ...issued.map((s) => ({
        client_id: s.client_id,
        event_type: s.event_type,
        secret: s.signing_secret,
      })),
    );
  }

  if (registered.length > 0) {
    const receiver = process.env.WEBHOOK_CONTROL_URL ?? 'http://localhost:3004';
    const res = await fetch(`${receiver}/secrets`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ secrets: registered }),
    });
    console.log(
      res.ok
        ? `\nhanded ${registered.length} signing secrets to the demo receiver`
        : `\ncould not reach the demo receiver at ${receiver} — signatures will show as unverified`,
    );
  }
}

main().catch((err: unknown) => {
  console.error('subscribe-all failed', err);
  process.exit(1);
});

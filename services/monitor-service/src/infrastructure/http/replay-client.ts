import type { ReplayClient, ReplayOutcome } from '../../application/ports.js';

/**
 * Replays through the public self-service API rather than touching the
 * database or Kafka directly.
 *
 * That is the point: an operator pressing "replay" on the dashboard takes the
 * exact same path a client would, so the action is authorised the same way and
 * lands in the same audit trail with `dispatch_source = SELF_SERVICE`. A tool
 * that reached past the API would be a second, unaudited way to trigger
 * deliveries — precisely the thing an operations team is meant to be able to
 * account for afterwards.
 */
export class SelfServiceReplayClient implements ReplayClient {
  constructor(
    private readonly baseUrl: string,
    private readonly tokenTtlMs = 60_000,
  ) {}

  private readonly tokens = new Map<string, { token: string; expiresAt: number }>();

  async replay(notificationEventId: string, clientId: string): Promise<ReplayOutcome> {
    const token = await this.tokenFor(clientId);

    const res = await fetch(
      `${this.baseUrl}/notification_events/${encodeURIComponent(notificationEventId)}/replay`,
      { method: 'POST', headers: { authorization: `Bearer ${token}` } },
    );

    return { ok: res.ok, status: res.status, body: await res.json().catch(() => null) };
  }

  /**
   * In the demo the token comes from the self-service demo endpoint. In a real
   * deployment the operations console would hold its own scoped credential and
   * act on behalf of a client explicitly, which is a change of one method.
   */
  private async tokenFor(clientId: string): Promise<string> {
    const cached = this.tokens.get(clientId);
    if (cached && cached.expiresAt > Date.now()) return cached.token;

    const res = await fetch(`${this.baseUrl}/auth/token`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ client_id: clientId }),
    });
    if (!res.ok) {
      throw new Error(`could not obtain an operations token: ${res.status}`);
    }

    const { access_token: token } = (await res.json()) as { access_token: string };
    this.tokens.set(clientId, { token, expiresAt: Date.now() + this.tokenTtlMs });
    return token;
  }
}

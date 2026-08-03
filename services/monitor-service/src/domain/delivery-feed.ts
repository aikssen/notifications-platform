/**
 * The case statement asks for two different things under one word:
 *
 *   "observable in a near real-time approach to allow our internal monitoring
 *    team to promptly identify behavior deviation and promptly respond to any
 *    client complaint about the notification delivery"
 *
 * "Identify behaviour deviation" is an aggregate question — is the platform
 * behaving differently than usual? That is Prometheus and Grafana.
 *
 * "Respond to a client complaint" is a specific question — what happened to
 * client X's event Y? No dashboard of rates answers that. This service does.
 *
 * They are different tools because they are different questions, and both are
 * fed by the same delivery-result stream.
 */

export type DeliveryStatus = 'SUCCESS' | 'FAILED';
export type DispatchSource = 'SYSTEM' | 'RETRY_SERVICE' | 'SELF_SERVICE';

export interface DeliveryResult {
  notificationEventId: string;
  eventId: string;
  clientId: string;
  eventType: string;
  state: string;
  status: DeliveryStatus;
  dispatchSource: DispatchSource;
  attemptNumber: number;
  webhookUrl: string;
  responseStatus: number | null;
  errorMessage: string | null;
  durationMs: number;
  correlationId: string | null;
  occurredAt: Date;
}

export interface Summary {
  total: number;
  succeeded: number;
  failed: number;
  successRate: number;
  byState: Record<string, number>;
  bySource: Record<string, number>;
  latency: { p50: number; p95: number; max: number };
  clients: number;
}

/**
 * A bounded in-memory view of recent deliveries.
 *
 * Deliberately not a database: this is a live operations window, and the
 * durable record already exists in PostgreSQL, queryable through the
 * self-service API. Holding a ring buffer means the monitor can be restarted,
 * scaled or removed without anyone losing data.
 */
export class DeliveryFeed {
  private readonly buffer: DeliveryResult[] = [];

  constructor(private readonly capacity = 500) {}

  record(result: DeliveryResult): void {
    this.buffer.push(result);
    if (this.buffer.length > this.capacity) {
      this.buffer.shift();
    }
  }

  recent(limit: number, clientId?: string): DeliveryResult[] {
    const source = clientId
      ? this.buffer.filter((r) => r.clientId === clientId)
      : this.buffer;
    return source.slice(-limit).reverse();
  }

  summary(clientId?: string): Summary {
    const items = clientId ? this.buffer.filter((r) => r.clientId === clientId) : this.buffer;

    const byState: Record<string, number> = {};
    const bySource: Record<string, number> = {};
    const clients = new Set<string>();
    const durations: number[] = [];
    let succeeded = 0;

    for (const item of items) {
      byState[item.state] = (byState[item.state] ?? 0) + 1;
      bySource[item.dispatchSource] = (bySource[item.dispatchSource] ?? 0) + 1;
      clients.add(item.clientId);
      if (item.durationMs > 0) durations.push(item.durationMs);
      if (item.status === 'SUCCESS') succeeded += 1;
    }

    durations.sort((a, b) => a - b);

    return {
      total: items.length,
      succeeded,
      failed: items.length - succeeded,
      successRate: items.length === 0 ? 1 : succeeded / items.length,
      byState,
      bySource,
      latency: {
        p50: percentile(durations, 0.5),
        p95: percentile(durations, 0.95),
        max: durations.at(-1) ?? 0,
      },
      clients: clients.size,
    };
  }
}

function percentile(sorted: number[], q: number): number {
  if (sorted.length === 0) return 0;
  const index = Math.min(sorted.length - 1, Math.floor(q * sorted.length));
  return sorted[index] ?? 0;
}

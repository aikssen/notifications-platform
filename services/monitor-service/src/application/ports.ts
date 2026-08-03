import type { DeliveryResult } from '../domain/delivery-feed.js';

/**
 * Hexagonal, lightly. One inbound port and two outbound ones is the whole
 * surface — this service has no persistence and one business rule, so the full
 * layering would be ceremony (ADR 0003).
 */

/** Inbound: something delivers results to the service. */
export interface ResultStream {
  start(handler: (result: DeliveryResult) => void): Promise<void>;
  stop(): Promise<void>;
}

/** Outbound: push a result to every connected dashboard. */
export interface Broadcaster {
  broadcast(result: DeliveryResult): void;
  clientCount(): number;
}

/**
 * Outbound: ask the self-service API to replay an event.
 *
 * The monitor does not replay anything itself. It calls the same public
 * endpoint a client would, so the operator's action goes through exactly the
 * same authorisation and the same audit trail — an operations tool that
 * bypassed the API would be a second, unaudited way to trigger deliveries.
 */
export interface ReplayClient {
  replay(notificationEventId: string, clientId: string): Promise<ReplayOutcome>;
}

export interface ReplayOutcome {
  ok: boolean;
  status: number;
  body: unknown;
}

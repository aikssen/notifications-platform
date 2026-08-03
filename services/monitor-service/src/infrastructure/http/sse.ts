import type { Request, Response } from 'express';
import type { Logger } from 'pino';

import type { DeliveryResult } from '../../domain/delivery-feed.js';
import type { Broadcaster } from '../../application/ports.js';

/**
 * Server-Sent Events, not WebSockets.
 *
 * The dashboard only ever receives; it has no upstream messages to send. SSE
 * gives that for free over plain HTTP: automatic reconnection is built into
 * every browser's EventSource, it passes through proxies and load balancers
 * that mishandle the WebSocket upgrade, and it needs no library on either end.
 *
 * Choosing WebSockets here would mean taking on a dependency, a heartbeat
 * implementation and a reconnection strategy to gain a channel direction that
 * is never used.
 */
export class SseBroadcaster implements Broadcaster {
  private readonly clients = new Set<Response>();

  constructor(private readonly log: Logger) {}

  handle(req: Request, res: Response): void {
    res.writeHead(200, {
      'Content-Type': 'text/event-stream',
      'Cache-Control': 'no-cache, no-transform',
      Connection: 'keep-alive',
      // Nginx buffers responses by default, which would hold events back until
      // the buffer fills — the one line that makes SSE appear broken in
      // production while working perfectly in development.
      'X-Accel-Buffering': 'no',
    });

    // Tells the browser how long to wait before reconnecting.
    res.write('retry: 3000\n\n');
    res.write(': connected\n\n');

    this.clients.add(res);
    this.log.info({ clients: this.clients.size }, 'dashboard connected');

    // A comment frame every 20 seconds. Idle connections are otherwise closed
    // by proxies and the dashboard silently stops updating.
    const heartbeat = setInterval(() => {
      res.write(': keep-alive\n\n');
    }, 20_000);

    req.on('close', () => {
      clearInterval(heartbeat);
      this.clients.delete(res);
      this.log.info({ clients: this.clients.size }, 'dashboard disconnected');
    });
  }

  broadcast(result: DeliveryResult): void {
    const frame = `event: delivery\ndata: ${JSON.stringify(result)}\n\n`;
    for (const client of this.clients) {
      client.write(frame);
    }
  }

  clientCount(): number {
    return this.clients.size;
  }
}

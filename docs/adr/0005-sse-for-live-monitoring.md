# 5. Server-Sent Events for the operations dashboard

**Status:** accepted

## Context

The case statement asks for near real-time observability so an internal team can
spot deviation and answer a client complaint. The second half needs a live view
of individual deliveries.

## Decision

`monitor-service` streams delivery results over SSE. The dashboard is one
self-contained HTML file.

## Consequences

The dashboard only ever receives; it has no upstream messages to send. SSE
provides exactly that over plain HTTP: `EventSource` reconnects on its own in
every browser, it passes through proxies and load balancers that mishandle the
WebSocket upgrade, and it needs no library on either end.

Choosing WebSockets would mean a dependency, a heartbeat implementation and a
reconnection strategy, to gain a channel direction that is never used. The
replay button is a plain `POST` — it does not need the socket.

Two production details are in the implementation because they are the ones that
make SSE look broken when missed: `X-Accel-Buffering: no`, without which nginx
holds events until its buffer fills, and a comment frame every twenty seconds,
without which idle connections are dropped and the page silently stops updating.

The dashboard has no CDN reference. A dashboard that needs the network to render
is a dashboard that fails on the venue's wifi, which is when it is being shown.

## Note

This is the *complaint* half of the requirement. The *deviation* half is
Prometheus and Grafana, fed from the same stream. They are different tools
because they answer different questions, and a rate percentage cannot answer
"what happened to this one event".

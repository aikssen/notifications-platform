# monitor-service

The near real-time operations view. Node + Express, hexagonal-light.

## Why this exists alongside Grafana

The case statement puts two different needs under one sentence:

> *"observable in a near real-time approach to allow our internal monitoring team
> to promptly identify **behavior deviation** and promptly respond to any
> **client complaint** about the notification delivery"*

| The need | The tool | Why |
|---|---|---|
| identify behaviour deviation | **Prometheus + Grafana** | rates, percentiles, trends, alerts |
| respond to a client complaint | **this service** | *"what happened to CLIENT002's EVT003?"* |

Grafana cannot answer the second one. A success rate of 98.4% tells an operator
nothing about the one delivery a client is on the phone about. That question
needs the individual event, its origin, its HTTP response and a way to act —
which is what this dashboard is.

Both are fed by the same `notifications.delivery-result` stream, so the numbers
on the Grafana board and the rows on the dashboard cannot disagree.

## Observability is a consumer, not a module

This service subscribes to a Kafka topic. It holds no database connection and
the delivery path has no reference to it. It can be restarted, scaled, replaced
or switched off entirely and nothing about delivery changes — because nothing
in the delivery path knows it exists.

That is the difference between an event-driven architecture and a diagram of
one, and it is worth pointing at during the presentation.

## Why SSE and not WebSockets

The dashboard only ever receives. It has no upstream messages to send.

Server-Sent Events give that over plain HTTP: `EventSource` reconnects on its
own in every browser, it passes through proxies that mishandle the WebSocket
upgrade, and it needs no library on either end. Choosing WebSockets here would
mean taking on a dependency, a heartbeat and a reconnection strategy to gain a
channel direction that is never used.

Two details that make it work in production and are easy to miss:
`X-Accel-Buffering: no`, without which nginx holds events back until its buffer
fills, and a comment frame every twenty seconds, without which idle connections
are closed and the dashboard silently stops updating.

## The dashboard is one self-contained file

`src/public/index.html` — no CDN, no bundler, no framework. A dashboard that
depends on a CDN is a dashboard that fails on the venue's wifi, which is
exactly when it is being demonstrated.

## Replay goes through the public API

The replay button calls `POST /notification_events/{id}/replay` on the
self-service API — the same endpoint a client would use. An operations tool
that reached past the API into Kafka or the database would be a second,
unaudited way to trigger deliveries. Going through the front door means the
operator's action is authorised the same way and lands in the same audit trail
as `dispatch_source = SELF_SERVICE`.

## Endpoints

| | |
|---|---|
| `GET /` | the dashboard |
| `GET /stream` | SSE feed of delivery results |
| `GET /api/summary?client_id=` | counters, success rate, latency percentiles |
| `GET /api/deliveries?client_id=&limit=` | recent deliveries |
| `POST /api/replay` | proxies a replay through the self-service API |
| `GET /healthz`, `GET /metrics` | |

## Configuration

| Variable | Default | |
|---|---|---|
| `KAFKA_BROKERS` | — | required |
| `KAFKA_TOPIC_RESULT` | `notifications.delivery-result` | |
| `SELF_SERVICE_BASE_URL` | `http://self-service-api:3002` | |
| `MONITOR_SERVICE_PORT` | `3003` | |
| `MONITOR_BUFFER` | `500` | ring buffer size — this is a live window, not a ledger |

# Notifications Platform

Event-driven webhook delivery for platform-generated events, with a client-facing self-service API.

Built for the Cobre *Sr. Software Engineer — Notifications* case.

---

## The problem

The platform emits business events (payments, transfers, balance updates). Each event has to reach
the right client's webhook, exactly once from the client's point of view, with a full audit trail,
an automatic recovery path when the webhook is down, and enough visibility that an internal
monitoring team can answer *"what happened to client X's event Y?"* in seconds.

The producers must not know that webhooks exist.

## How it works

```
  platform microservices ──▶ Kafka: notifications.dispatch
                                       │
                                       ▼
                             dispatch-service (Go)
                       ┌───────────────┼───────────────┐
                       ▼               ▼               ▼
             subscription-service   PostgreSQL    client webhook
              (is this event      (events +       (HMAC-signed,
               deliverable?)       attempts)       SSRF-guarded)
                                       │
                                       ▼
                             retry-service (Go)
                       exponential backoff ──▶ Kafka (RETRY_SERVICE)
                       budget exhausted   ──▶ Kafka: notifications.dlq
                                              + state = FAILED (replayable)

  client ──▶ self-service-api ──▶ PostgreSQL
                    └── replay ──▶ Kafka (SELF_SERVICE)

  dispatch ──▶ Kafka: notifications.delivery-result ──▶ monitor-service ──SSE──▶ ops dashboard
  every service ──▶ /metrics ──▶ Prometheus ──▶ Grafana
```

Three properties are worth calling out:

**One delivery pipeline.** A first delivery, an automatic retry and a manual replay are the same
code path. They differ only in `dispatch_source` (`SYSTEM` / `RETRY_SERVICE` / `SELF_SERVICE`),
which keeps the audit trail honest and means retry logic cannot drift from delivery logic.

**Observability is a consumer, not a module.** `monitor-service` subscribes to the delivery-result
stream. It can be replaced, scaled or removed without touching a line of the delivery path.

**The dependency rule is enforced by CI.** See [ARCHITECTURE.md](ARCHITECTURE.md).

## Services

| Service | Language | What it does |
|---|---|---|
| [`dispatch-service`](services/dispatch-service) | Go | Consumes events, resolves the subscription, calls the webhook, persists the outcome |
| [`retry-service`](services/retry-service) | Go | Re-enqueues due events with exponential backoff; exhausts to the DLQ |
| [`self-service-api`](services/self-service-api) | Node + Express | `GET /notification_events`, detail, and `POST .../replay` |
| [`subscription-service`](services/subscription-service) | Node + Express | Subscription CRUD, resolution, webhook URL validation |
| [`monitor-service`](services/monitor-service) | Node + Express | Live operations dashboard over SSE |
| [`demo-tools`](services/demo-tools) | Node | Seeds the fixture, simulates the platform and a client webhook |

## Quick start

Requires Docker, Node 22+, pnpm and Go 1.24+.

```bash
cp .env.example .env
make up             # postgres + kafka (KRaft) + prometheus + grafana + 5 services
make subscribe-all  # register a subscription per client and event type
make deliver-all    # push every fixture event through the real pipeline
./scripts/e2e.sh    # 36 assertions against the running stack
```

| | |
|---|---|
| Operations dashboard | http://localhost:3003 |
| Grafana | http://localhost:3000/d/notifications-delivery |
| Prometheus | http://localhost:9090 |
| Self-service API | http://localhost:3002 |
| Subscription API | http://localhost:3001 |

`make help` lists every target.

## Verification

```bash
make test              # unit tests: 85 in TypeScript, 60+ in Go
make test-integration  # real PostgreSQL via testcontainers
make lint              # includes the dependency rule, in both languages
./scripts/e2e.sh       # the whole pipeline against a running stack
```

The end-to-end script walks the same path the demo does and asserts on the
result — SSRF refusals, tenant isolation, the ten fixture events delivered and
signed, both list filters, a failure retried to exhaustion, and a replay
arriving back through the same pipeline.

## Documentation

| | |
|---|---|
| [ARCHITECTURE.md](ARCHITECTURE.md) | three layers, one rule, and the lint that enforces it |
| [docs/diagrams/](docs/diagrams) | architecture, lifecycle, sequences, data model — as Mermaid |
| [docs/adr/](docs/adr) | seven decisions with their trade-offs and rejected alternatives |
| [docs/security-owasp.md](docs/security-owasp.md) | each control mapped to a file and line, plus the honest gaps |
| [docs/api/openapi.yaml](docs/api/openapi.yaml) | both client APIs |
| [docs/DEMO_RUNBOOK.md](docs/DEMO_RUNBOOK.md) | a deterministic twelve-minute demo |
| [docs/PRESENTATION_SCRIPT.md](docs/PRESENTATION_SCRIPT.md) | slide-by-slide script and a question bank |

## About the fixture

`fixtures/notification_events.json` is the file attached to the case statement, unmodified. It is
used in three ways:

1. **Initial state** — `make seed` loads all ten events with their original outcome
   (seven `DELIVERED`, three `FAILED`), so the self-service filters and the replay flow are
   demonstrated against the dataset the case itself provides.
2. **An alternative repository adapter** — setting `EVENTS_REPOSITORY=file` makes the
   self-service API read from the JSON file instead of PostgreSQL, through the same port.
3. **A delivery source** — `make deliver-all` pushes every event through the live pipeline to the
   configured webhook URL.

Its `EVT001` / `CLIENT001` identifiers load verbatim: upstream identifiers are stored as opaque
indexed strings, because their format is not ours to choose.

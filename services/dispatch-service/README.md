# dispatch-service

The delivery worker. Consumes `notifications.dispatch`, confirms the event is
deliverable, calls the client webhook, and stores the outcome.

No public API — only `/healthz`, `/readyz` and `/metrics` on the operations port.

## What it does, in the order the case statement asks for it

1. **Ingest, exactly once.** `event_id` is the idempotency key, enforced by a
   unique constraint. Two instances ingesting the same event concurrently is
   handled explicitly: the loser of the insert race adopts the winner's row.
2. **Confirm with the subscription.** The lookup is keyed on
   `(client_id, event_type)` together — never `event_type` alone. That pairing
   is what enforces *"notifications sent to every client belong to events
   generated to that client"* at the delivery layer, and an answer for a
   different client is refused rather than delivered on.
3. **Deliver.** Signed with HMAC-SHA256 over `timestamp + "." + body`, sent
   through an SSRF-guarded HTTP client that does not follow redirects.
4. **Handle failure.** Transient failures are retried in place with exponential
   backoff and full jitter. Client errors are not retried — a 400 is an answer.
5. **Store the outcome.** The attempt and the resulting event state are written
   in one transaction.

## Two decisions worth knowing about

**It does not decide whether to retry.** A failed delivery becomes `RETRYING`,
full stop. Whether the retry budget is spent, and when the next attempt should
happen, is the retry service's policy. This service only answers *did it work?*

**Offsets are committed manually, after processing succeeds.** A message that
cannot be processed is retried in place, applying backpressure and surfacing as
consumer lag. A message that cannot even be parsed goes to the dead letter
queue — blocking a partition on one malformed message would stall every other
client's notifications.

## Layout

```
cmd/dispatcher/main.go       composition root — the only file naming an adapter
internal/domain/             entities, value objects, the state machine
internal/application/
    port/ports.go            every port, one file
    usecase/                 ingest, dispatch, process
internal/infrastructure/
    messaging/               Kafka consumer (manual commit) and publishers
    postgres/                event store
    subscriptions/           subscription resolution over HTTP
    webhook/                 delivery, HMAC signing, SSRF guard
    observability/           logging, metrics, health
    config/, system/
```

The dependency rule is enforced by `depguard` in `.golangci.yml`. Importing
`pgx` from `internal/domain` fails `golangci-lint run`.

## Running the tests

```bash
go test ./...                      # domain and use cases, no infrastructure
go test -tags=integration ./...    # real PostgreSQL via testcontainers
golangci-lint run ./...            # includes the dependency rule
```

The integration tests exist for the things a fake cannot prove: that only one
dispatcher can claim an event, and that attempt numbering holds under
concurrent writers. The second one caught a real defect — an optimistic retry
loop that a thundering herd of writers exhausted — which is now fixed with a
row lock on the parent event.

## Configuration

| Variable | Default | |
|---|---|---|
| `DATABASE_URL` | — | required |
| `KAFKA_BROKERS` | — | required, comma-separated |
| `SUBSCRIPTIONS_BASE_URL` | — | required |
| `KAFKA_TOPIC_DISPATCH` | `notifications.dispatch` | |
| `KAFKA_TOPIC_RESULT` | `notifications.delivery-result` | |
| `KAFKA_TOPIC_DLQ` | `notifications.dlq` | |
| `KAFKA_CONSUMER_GROUP` | `notifications-dispatch` | |
| `WEBHOOK_TIMEOUT_MS` | `5000` | |
| `WEBHOOK_SYNC_MAX_ATTEMPTS` | `3` | in-process attempts, not persisted |
| `WEBHOOK_SYNC_BASE_DELAY_MS` | `200` | |
| `WEBHOOK_SYNC_MAX_DELAY_MS` | `2000` | |
| `WEBHOOK_REQUIRE_HTTPS` | `true` | **relaxing this is logged as a warning** |
| `WEBHOOK_ALLOW_PRIVATE_NETWORKS` | `false` | **relaxing this is logged as a warning** |
| `DISPATCH_METRICS_PORT` | `9101` | |
| `LOG_LEVEL` | `info` | |

`DATABASE_URL` is never logged: a connection string is a password.

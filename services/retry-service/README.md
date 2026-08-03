# retry-service

The asynchronous retry worker. Polls for events whose delivery failed,
re-publishes them, and declares them definitively failed once the budget is
spent.

No public API — only `/healthz`, `/readyz` and `/metrics`.

## Why this is a separate service

The dispatcher answers *did the delivery work?* This one answers *should we try
again, and when?* Splitting them means the retry policy has exactly one owner.
Fold it back into the dispatcher and "how many retries do we allow" quietly
gets three different answers across the codebase.

It is also why `FAILED` is reachable at all. The dispatcher never writes it;
this service does, when the budget runs out. That transition is what the case
statement's *"re-send a notification when delivery has definitely failed"*
requirement acts on.

## The loop

1. **Claim** what is due, with `FOR UPDATE SKIP LOCKED`.
2. **Decide** per event: requeue with backoff, or exhaust.
3. **Requeue** onto `notifications.dispatch` with `dispatch_source=RETRY_SERVICE`
   — the identical pipeline a first delivery takes.
4. **Exhaust** to `FAILED` plus a record on `notifications.dlq`.
5. **Sweep** for deliveries abandoned mid-flight.

## Four decisions worth knowing about

**A visibility window, not a held lock.** The claim pushes `next_retry_at`
forward by the visibility timeout, which hides the row from other pollers. That
way a database transaction is never held open across a Kafka publish. If this
process dies mid-cycle, the window lapses and the event becomes due again on
its own — no cleanup, no stuck rows.

**`SKIP LOCKED`, so instances add throughput.** Concurrent pollers walk past
rows another poller is reading rather than blocking behind them. Each gets a
disjoint batch. An integration test runs eight pollers against forty events and
asserts no event is claimed twice.

**Publish before scheduling.** If the publish succeeds and the schedule write
then fails, the event is delivered and becomes due again when the window
lapses — a duplicate attempt, which idempotency and the dispatcher's claim
absorb. The reverse order risks an event marked as scheduled that was never
actually published.

**Full jitter, not a wobble.** The delay is drawn uniformly from the whole
backoff window. When a client's endpoint recovers after an outage, the hundreds
of events waiting on it must not all fire in the same instant and knock it over
again.

**The stalled-delivery sweep.** A dispatcher that dies after claiming an event
leaves it in `DELIVERING`, where nothing will ever look at it again: the
dispatcher skips `DELIVERING` events by design, and this poller only reads
`RETRYING` ones. Without the sweep, a single crash strands a client's
notification permanently.

## Why a database poller and not Kafka delay topics

Delay topics (`retry-5s`, `retry-1m`, `retry-5m`) avoid polling the database,
but they fix the backoff schedule into topology: changing the curve means
changing infrastructure. The poller keeps the policy in code, keeps the state
queryable by the self-service API, and reuses the partial index that already
exists. At a volume where polling becomes the bottleneck, delay topics are the
right next step — see [ADR 0004](../../docs/adr/0004-db-poller-retry.md).

## Running the tests

```bash
go test ./...                      # the policy and the loop, no infrastructure
go test -tags=integration ./...    # real PostgreSQL via testcontainers
golangci-lint run ./...            # includes the dependency rule
```

## Configuration

| Variable | Default | |
|---|---|---|
| `DATABASE_URL` | — | required |
| `KAFKA_BROKERS` | — | required |
| `RETRY_MAX_ATTEMPTS` | `5` | retries before `FAILED` |
| `RETRY_BASE_DELAY_SECONDS` | `10` | |
| `RETRY_MAX_DELAY_SECONDS` | `900` | backoff cap |
| `RETRY_POLL_INTERVAL_SECONDS` | `5` | idle cadence; a full batch polls again immediately |
| `RETRY_BATCH_SIZE` | `50` | |
| `RETRY_VISIBILITY_SECONDS` | `60` | must exceed the poll interval — validated at startup |
| `RETRY_STALLED_AFTER_SECONDS` | `300` | when a `DELIVERING` event counts as abandoned |
| `RETRY_METRICS_PORT` | `9102` | |

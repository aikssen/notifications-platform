# self-service-api

The client-facing REST API. Node + Express, hexagonal.

Three endpoints, taken directly from the case statement:

| | |
|---|---|
| `GET /notification_events` | list, filtered by creation date and delivery status |
| `GET /notification_events/{id}` | one event with its full delivery history |
| `POST /notification_events/{id}/replay` | re-send a delivery that definitively failed |

## What changed, and why it mattered

**The replay guard was inverted.** The brief asks to *"re-send a notification
when delivery has definitely failed"*. The previous implementation rejected
`FAILED` and accepted `PENDING` and `RETRYING`. It went unnoticed because the
dispatcher never wrote `FAILED` either, so the branch was unreachable and
nothing ever contradicted it. Both halves are fixed: the retry worker now
declares exhaustion, and this endpoint acts on exactly that state.

**The filters did not exist.** The list took a client id and returned
everything. Both filters the brief names are implemented, plus pagination with
a capped page size — `delivery_status` is accepted as an alias for `state`,
since that is the brief's own vocabulary and the fixture's.

**The detail endpoint returned any client's data.** It fetched by id and never
compared the owner, so any authenticated caller could read another client's
payment payloads by guessing an id. The client identity is now part of the
lookup rather than a check applied afterwards, so it cannot be forgotten at a
call site — and a foreign id answers `404`, not `403`, because a 403 confirms
the resource exists.

**The mapper dropped fields silently.** It read `row.webhookUrl` off a
snake_case row, so `webhook_url`, `request_method` and `request_payload` came
back `undefined` in the detail response — the one endpoint most likely to be
demonstrated. There is a test for exactly those three fields now.

## Two adapters behind one port

`NotificationEventRepository` has two implementations:

- `PostgresNotificationEventRepository` — the real one.
- `FileNotificationEventRepository` — reads `fixtures/notification_events.json`,
  the file attached to the case statement, unmodified.

```bash
EVENTS_REPOSITORY=file pnpm dev
```

Every endpoint answers identically. Nothing else in the service changes, and
neither use case can tell which adapter it received.

That is the point of the brief's own wording — *"obtain the list of
notifications from **a repository** (the attached file …)"* — in a task that
asks for hexagonal architecture. The file is not a data format to import; it is
one implementation of the repository port.

The file adapter is read-only, and `markForReplay` is simply absent from it
rather than throwing. Replay therefore answers `501` in file mode as a
type-level consequence, not a runtime flag.

The three `failed` events in the fixture (`EVT003`, `EVT005`, `EVT009`) are the
ones the replay requirement acts on, and `EVT001` / `CLIENT001` load verbatim —
upstream identifiers are opaque strings, so nothing has to be rewritten to fit
a UUID column.

## Replay is not a second delivery path

The endpoint publishes the original event back onto the same Kafka topic a
first delivery arrives on, tagged `dispatch_source = SELF_SERVICE`. There is no
second implementation of delivery that could drift from the first — a manual
replay, an automatic retry and an initial send are one code path with three
origins, and the origin exists only so the audit trail can tell them apart.

The state is moved before the publish. If the publish fails, the event is left
in `RETRYING` and the retry worker picks it up on its next pass — a lost
publish degrades into a slightly later retry rather than a replay the client
was told happened and never did.

## Running the tests

```bash
pnpm test        # 31 cases, including the fixture adapter against the real file
pnpm typecheck
pnpm lint        # includes the dependency rule
```

## Configuration

| Variable | Default | |
|---|---|---|
| `JWT_SECRET` | — | required, minimum 16 characters |
| `DATABASE_URL` | — | required unless `EVENTS_REPOSITORY=file` |
| `KAFKA_BROKERS` | — | required for replay |
| `EVENTS_REPOSITORY` | `postgres` | `postgres` or `file` |
| `FIXTURE_PATH` | `../../fixtures/notification_events.json` | |
| `SELF_SERVICE_PORT` | `3002` | |
| `ENABLE_DEMO_TOKENS` | `false` | mounts `POST /auth/token` |

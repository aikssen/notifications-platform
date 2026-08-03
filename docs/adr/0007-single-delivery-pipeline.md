# 7. One delivery pipeline, three origins

**Status:** accepted

## Context

There are three reasons a notification gets sent: the platform produced an
event, an earlier attempt failed and is being retried, or a client asked for a
redelivery.

The obvious implementation gives each its own path. That is also how the retry
path ends up not signing requests, and the replay path ends up not honouring
`expected_status`, six months after everyone agreed they should.

## Decision

All three publish to `notifications.dispatch` and are consumed by the same
handler. They differ in exactly one field, `dispatch_source`:
`SYSTEM`, `RETRY_SERVICE`, `SELF_SERVICE`.

That value is carried into `notification_attempts`, so the audit trail records
why each attempt happened.

## Consequences

There is one implementation of delivery. A change to signing, to timeout
handling, to SSRF validation, applies to all three by construction rather than
by discipline.

The audit trail is legible. A real run:

| attempt | dispatch_source | status |
|---|---|---|
| 1 | `SYSTEM` | FAILED |
| 2–5 | `RETRY_SERVICE` | FAILED |
| 6 | `SELF_SERVICE` | SUCCESS |

Six rows, three origins, one pipeline — the whole story of one notification, in
one query.

The cost is that a replay is asynchronous. `POST /replay` answers `202` and the
delivery happens shortly after; a client polls or waits for the webhook. That is
the honest response anyway: a synchronous answer would mean holding an HTTP
connection open while calling a third party.

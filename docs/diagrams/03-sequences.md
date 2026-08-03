# Sequences

## Delivery, including failure and recovery

```mermaid
sequenceDiagram
  autonumber
  participant P as Platform
  participant K as Kafka
  participant D as dispatch (Go)
  participant S as subscriptions
  participant W as Client webhook
  participant DB as PostgreSQL
  participant R as retry (Go)

  P->>K: publish(event, key=client_id)
  K->>D: fetch (offset NOT committed)

  D->>DB: find by event_id
  Note over D,DB: idempotency boundary —<br/>a redelivery creates nothing new
  D->>DB: claim: PENDING → DELIVERING
  Note over D,DB: compare-and-set. A second dispatcher<br/>loses here and steps aside.

  D->>S: GET /internal/subscriptions/resolve<br/>client_id + event_type
  S-->>D: webhook, method, expected_status, secret

  D->>W: POST, X-Signature: sha256(ts + "." + body)
  W-->>D: 500

  Note over D,W: in-process retries with exponential<br/>backoff and full jitter — not persisted,<br/>they are transport noise

  D->>DB: attempt + state → RETRYING (one transaction)
  D->>K: publish delivery result
  D->>K: commit offset

  loop until delivered or the budget runs out
    R->>DB: claim due, FOR UPDATE SKIP LOCKED
    Note over R,DB: the claim pushes next_retry_at forward<br/>by the visibility window, so no DB lock<br/>is held across a Kafka publish
    R->>K: requeue, dispatch_source=RETRY_SERVICE
    K->>D: fetch
    D->>W: POST
  end

  R->>DB: budget spent → FAILED
  R->>K: dead letter record
  Note over R,DB: FAILED is what the client can replay from
```

## Replay, requested by the client

```mermaid
sequenceDiagram
  autonumber
  participant C as Client
  participant A as self-service-api
  participant DB as PostgreSQL
  participant K as Kafka
  participant D as dispatch (Go)
  participant W as Client webhook

  C->>A: POST /notification_events/{id}/replay<br/>Authorization: Bearer …

  A->>A: client_id from the token claim
  Note over A: never from the query string or body —<br/>that was the cross-tenant read

  A->>DB: find by (id, client_id)
  Note over A,DB: ownership is part of the lookup,<br/>so it cannot be forgotten at a call site
  DB-->>A: event, state = FAILED

  A->>A: canBeReplayed() — FAILED only
  Note over A: 409 with an explanation for any<br/>other state; 404 for another client's id

  A->>DB: FAILED → RETRYING
  A->>K: publish original payload<br/>dispatch_source=SELF_SERVICE
  A-->>C: 202 { state: "RETRYING" }

  K->>D: the same consumer, the same code
  D->>W: POST, signed
  W-->>D: 200
  D->>DB: attempt 6 · SELF_SERVICE · SUCCESS<br/>state = DELIVERED
```

The audit trail after that sequence, taken from a real run:

| attempt | dispatch_source | status |
|---|---|---|
| 1 | `SYSTEM` | FAILED |
| 2 | `RETRY_SERVICE` | FAILED |
| 3 | `RETRY_SERVICE` | FAILED |
| 4 | `RETRY_SERVICE` | FAILED |
| 5 | `RETRY_SERVICE` | FAILED |
| 6 | `SELF_SERVICE` | SUCCESS |

Six rows, three origins, one pipeline. That table is the architecture.

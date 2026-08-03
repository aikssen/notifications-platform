# Architecture

## The one rule

> **`domain` imports nothing. `application` imports only `domain`. `infrastructure` imports both.
> Never the other way around.**

Hexagonal, DDD and Clean Architecture are not three architectures stacked on top of each other.
They are three vocabularies for the same decision:

| Vocabulary | What it contributes |
|---|---|
| **Clean** | the *dependency rule* — which way the arrows point |
| **Hexagonal** | the *name of the edges* — ports and adapters |
| **DDD (tactical)** | the *content of the centre* — entities, value objects, invariants |

Three layers. One rule. That is the whole thing.

## The rule is enforced by lint, not by convention

A dependency rule that lives only in a README decays on the first deadline. Here it fails the build:

- **Go** — `depguard` inside `golangci-lint` denies `internal/infrastructure` and every
  infrastructure library (`pgx`, `kafka-go`, `net/http`, …) from being imported by
  `internal/domain` or `internal/application`.
- **TypeScript** — `import/no-restricted-paths` denies the same edges.

```bash
make lint   # fails if anyone imports pgx from the domain
```

## The same tree in two languages

The structure is identical in Go and in TypeScript. That is the point: the architecture does not
depend on the technology.

```
dispatch-service (Go)                     self-service-api (TypeScript)
├── cmd/dispatcher/main.go                ├── src/main.ts                    ← composition root
│
├── internal/domain/                      ├── src/domain/
│   ├── notification_event.go             │   ├── notificationEvent.ts       ← state machine
│   ├── notification_attempt.go           │   ├── notificationAttempt.ts
│   ├── values.go                         │   ├── values.ts                  ← the 3 value objects
│   └── errors.go                         │   └── errors.ts
│
├── internal/application/                 ├── src/application/
│   ├── port/ports.go                     │   ├── ports.ts                   ← every port, one file
│   └── usecase/                          │   └── usecase/                   ← 1 use case = 1 class
│
└── internal/infrastructure/              └── src/infrastructure/
    ├── kafka/                                ├── kafka/
    ├── postgres/                             ├── postgres/
    ├── http/                                 ├── http/
    ├── observability/                        ├── observability/
    └── config/                               └── config/
```

Every port lives in **one file**. The whole infrastructure surface of a service fits on one screen.

## Five rules that keep it small

1. **A port only where there is a real infrastructure decision.**
   If something will never have a second implementation and does not need to be substituted in
   tests, it is a function — not a port. `dispatch-service` has exactly six ports.

2. **Tactical DDD, not strategic ceremony.**
   Two entities, three value objects, **one strong invariant** (the delivery state machine).
   No nested aggregates. No internal domain events — the events already live in Kafka, and
   duplicating them inside the process would be ritual, not design. No generic repositories.

3. **Manual constructor injection in a `main` that reads top to bottom.**
   No DI container, no decorators, no `reflect-metadata`. The wiring is a file you read, not a
   convention you have to learn.

4. **DTOs at the edges, entities inside.**
   An HTTP response is a contract with a consumer. Serialising a domain entity straight to JSON
   couples that contract to the domain, and every future refactor becomes a breaking API change.

5. **Do not apply it where it does not pay, and say so.**
   `demo-tools` is a set of scripts, not a hexagonal service. `monitor-service` uses a light
   version (one inbound port, two outbound). This is recorded in
   [ADR 0003](docs/adr/0003-hexagonal-boundaries.md) as a deliberate choice.

## "Isn't this over-engineered?"

The centre is two entities and a five-state machine. The structure exists so that the state
machine can be tested without Kafka or PostgreSQL, and so that the linter guarantees it stays
that way. Remove the structure and the first infrastructure change rewrites the business rules.

## Service map

| Service | Language | Role | Hexagonal |
|---|---|---|---|
| `dispatch-service` | Go | Kafka consumer, resolves subscription, calls webhook, persists outcome | full |
| `retry-service` | Go | Polls due events, re-enqueues with backoff, exhausts to DLQ | full |
| `self-service-api` | Node + Express | Client REST API: list, detail, replay | full |
| `subscription-service` | Node + Express | Subscription CRUD, resolution, SSRF validation | full |
| `monitor-service` | Node + Express | Consumes delivery results, streams to the ops dashboard over SSE | light |
| `demo-tools` | Node | Seeds the fixture, simulates the platform and the client webhook | none (scripts) |

Go is used for the two workers: they are pure throughput components with no public HTTP surface,
where goroutine-per-message concurrency, a small memory footprint and a static binary are worth
having. The APIs stay on Node + Express, which is what the case statement asks for.
See [ADR 0002](docs/adr/0002-go-for-delivery-workers.md).

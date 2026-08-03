# 3. Three layers, one rule, and where not to apply it

**Status:** accepted

## Context

Hexagonal architecture applied by ceremony produces services where finding the
line that does the work takes ten minutes. Applied not at all, it produces
domain logic that cannot be tested without Kafka.

## Decision

Three layers — `domain`, `application`, `infrastructure` — and one rule:

> `domain` imports nothing. `application` imports only `domain`.
> `infrastructure` imports both.

The rule is enforced by `depguard` (Go) and `import/no-restricted-paths`
(TypeScript). Importing `pgx` from the domain fails `make lint`.

Five constraints keep it small:

1. A port only where there is a real infrastructure decision. Six in the
   dispatcher, and that is all.
2. Tactical DDD only: two entities, three value objects, one strong invariant
   (the state machine). No internal domain events — the events already live in
   Kafka, and duplicating them inside the process would be ritual.
3. Manual constructor injection in a `main` that reads top to bottom. No
   container, no decorators, no reflection.
4. DTOs at the edges. An HTTP response is a contract with a consumer;
   serialising a domain entity couples that contract to the domain.
5. Do not apply it where it does not pay.

## Where it is not applied, and why

**`demo-tools`** is a set of scripts that simulate the platform's producers and
a client's endpoint. It is not a product service. Giving it ports and use cases
would add files without adding a single thing anyone could change more easily.

**`monitor-service`** uses a light version: one inbound port and two outbound
ones. It has no persistence and one business rule, so a full application layer
would be a folder containing one function.

**Auth middleware is duplicated** between the two Node APIs rather than shared.
A security boundary that several services import from one package moves at the
speed of the slowest deploy. Eighty duplicated lines is the cheaper trade.

## Consequences

The dispatcher's entire infrastructure surface is one file, `ports.go`, that
fits on a screen. Every use case is tested with no Kafka, no PostgreSQL and no
HTTP server. The linter guarantees it stays that way.

The centre is two entities and a five-state machine. If that sounds like too
little to justify the structure: the structure exists so that machine stays
testable, and the first infrastructure change does not rewrite it.

# 4. A database poller for retries, not Kafka delay topics

**Status:** accepted

## Context

A failed delivery has to be retried later, with a growing delay, up to a limit.
Kafka has no native per-message delay.

## Decision

`retry-service` polls PostgreSQL for events whose `next_retry_at` has passed,
claims them with `FOR UPDATE SKIP LOCKED`, and republishes them onto the same
delivery topic.

The claim advances `next_retry_at` by a visibility window rather than holding a
lock, so no database transaction spans a Kafka publish. A process that dies
mid-cycle leaves nothing stuck: the window lapses and the event becomes due
again.

## Consequences

The backoff curve is a value in code, not a topology. Changing it is a
configuration change; with delay topics it is an infrastructure change.

The state stays queryable, which is what lets the self-service API answer *"what
is happening to my event"* from the same rows the retry loop reads. Two sources
of truth would have to be reconciled.

The cost is a poll every few seconds against a partial index. At the volume this
platform is sized for, that is nothing. It would stop being nothing at hundreds
of thousands of events in retry simultaneously.

`SKIP LOCKED` is what makes it horizontal: concurrent pollers walk past rows
another poller is reading rather than blocking behind them, so each instance
gets a disjoint batch. An integration test runs eight pollers against forty
events and asserts no event is claimed twice.

## Alternative

**Delay topics** (`retry-5s`, `retry-1m`, `retry-5m`, …) with a consumer per
tier. No database polling, and the natural answer at high volume. Rejected here
because it fixes the schedule into topology, splits the state across Kafka and
PostgreSQL, and adds five consumers to a system that does not yet need them.

It is the right next step, and the migration is contained: `retry-service` is
the only thing that would change.

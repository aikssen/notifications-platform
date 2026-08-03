# 1. Kafka between producers and delivery

**Status:** accepted

## Context

Platform microservices generate business events. Those events must reach client
webhooks — endpoints we do not control, which time out, return 500, and go down
for hours.

The naive option is for the payments service to call the client's webhook
directly. It is also the option that couples a payment's latency to a stranger's
uptime.

## Decision

Producers publish to `notifications.dispatch` and know nothing else. Delivery is
a separate concern with its own service, its own failure modes and its own
scaling.

Messages are keyed by `client_id`, so one client's events land on one partition
and keep their order relative to each other, while the topic still fans out
across partitions for throughput.

## Consequences

A slow webhook cannot slow down a payment. A delivery outage becomes consumer
lag — visible, bounded, and recoverable — instead of failed transactions.

Delivery is at-least-once, so idempotency is mandatory rather than optional.
That cost is paid explicitly: `event_id` is a unique constraint, and the
dispatcher claims an event with a compare-and-set before doing any outbound
work.

Adding a second delivery channel later (email, SMS) means adding a consumer, not
touching a producer.

## Alternatives

**A queue instead of a log.** RabbitMQ or SQS would deliver the events. Kafka's
retained log additionally allows replaying a window of traffic after a bug — a
capability worth having in a financial platform.

**Synchronous HTTP from producers.** Simplest to write, and it makes every
client's webhook a dependency of every payment. Rejected.

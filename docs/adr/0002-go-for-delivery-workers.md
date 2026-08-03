# 2. Go for the delivery workers, Node for the APIs

**Status:** accepted

## Context

The case statement asks for hexagonal architecture with Node and Express. It
also asks for an efficient retry strategy and near real-time observability —
requirements about throughput and operational behaviour.

## Decision

`dispatch-service` and `retry-service` are Go. `self-service-api`,
`subscription-service` and `monitor-service` are Node and Express.

## Consequences

The split follows the shape of the work rather than a language preference.

The two Go services are pure throughput components with no public HTTP surface.
Delivery is I/O-bound fan-out to endpoints that may each hang for seconds:
goroutine-per-message handles that with a scheduler rather than a callback
graph, and a static binary in a distroless image gives a small, boring
deployment unit with no runtime to patch.

The three Node services are request/response APIs shaped by JSON contracts,
validation and auth middleware — exactly what Express is good at, and what the
brief asks for.

The architecture is identical on both sides. `ARCHITECTURE.md` shows the same
directory tree in the two languages, which is the point: the structure does not
depend on the runtime.

## Risk, stated plainly

The brief says "nodejs - express". Reading that as *the API implementation task
must be Node and Express* is satisfied — the API implementation task is Node and
Express. Reading it as *every process must be Node* is not.

If the panel intends the stricter reading, the answer is that the boundary is a
port: `WebhookSender` and `EventStore` are interfaces, and porting the dispatcher
back to TypeScript is a rewrite of the adapters, not of the domain. The Node
services in this repository demonstrate the identical structure.

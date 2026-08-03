# Presentation script

Twelve slides, roughly 20 minutes of speaking, then the demo, then questions.

The material is ordered so that each slide earns the next. Diagrams come from
`docs/diagrams/` — render the Mermaid and export.

---

## Slide 1 · Title

**Notifications — event-driven webhook delivery**
Ever Cifuentes · Sr. Software Engineer

> "The hard part of this problem is not making an HTTP request. It is making it
> to the right client, proving afterwards that you did, recovering when the
> other end is down, and being able to answer a support call about one specific
> event six weeks later."

---

## Slide 2 · What the brief actually asks for

Five delivery requirements and three API capabilities, verbatim. Then:

> "I want to be explicit about one thing up front. I presented a version of this
> before, and it had a real defect: the replay endpoint rejected exactly the
> events it was supposed to act on. I will show you how that happened, because
> the *why* is more interesting than the fix."

**This lands well. It costs nothing — a technical panel will find it anyway —
and it buys credibility for everything after.**

---

## Slide 3 · The architecture

`docs/diagrams/01-architecture.md`.

Walk the numbered path once: publish → consume → resolve → deliver → record →
result. Then the retry loop. Then replay.

Three sentences, then stop:

> "The producer knows nothing. It publishes an event; it does not know webhooks
> exist, who is subscribed, or whether anything failed.
>
> There is one delivery pipeline. A first send, an automatic retry and a client
> replay all enter through the same topic and run the same code.
>
> Observability is a consumer of that stream, not a module inside it."

---

## Slide 4 · Why Go for two of the six services

> "The brief says Node and Express, and the API implementation is Node and
> Express. The two workers are Go, and I want to justify that rather than
> assume it.
>
> Those two have no public HTTP surface. They are throughput components doing
> I/O-bound fan-out to endpoints that each may hang for seconds.
> Goroutine-per-message handles that with a scheduler; a static binary in a
> distroless image gives a deployment unit with no runtime to patch.
>
> The architecture is identical on both sides —"

→ **the two-column slide**: the same directory tree in Go and TypeScript.

> "— which is the point. The structure does not depend on the runtime."

*If challenged:* the boundary is a port. Porting the dispatcher back to
TypeScript rewrites adapters, not the domain. The three Node services in the
repository demonstrate the same structure.

---

## Slide 5 · Three layers, one rule

> "Hexagonal, DDD and Clean are not three architectures stacked up. They are
> three vocabularies for one decision. Clean gives the dependency rule,
> hexagonal names the edges, DDD fills the centre."

The rule, large:

> **`domain` imports nothing. `application` imports only `domain`.
> `infrastructure` imports both.**

> "And it is not a convention in a README —"

→ **the lint output**:

```
internal/domain/values.go:6:2: import 'github.com/jackc/pgx/v5'
  is not allowed from list 'domain' (depguard)
```

> "— it fails the build. In both languages."

**Pre-empt the over-engineering question here, before it is asked:**

> "The centre is two entities and a five-state machine. The structure exists so
> that machine stays testable without Kafka or PostgreSQL, and so the linter
> keeps it that way. `demo-tools` has none of this, deliberately — knowing where
> not to apply it is part of applying it."

---

## Slide 6 · The whole infrastructure surface, on one screen

`ports.go` in full. Six outbound ports, one inbound.

> "That is everything the delivery logic can reach. Nothing in this file
> mentions Kafka, PostgreSQL or HTTP."

---

## Slide 7 · The lifecycle

`docs/diagrams/02-lifecycle.md`, then the transition table as code.

> "Every legal edge, in one map, with a test for every illegal one.
>
> Two of these matter. `DELIVERING` is explicit, which is how two dispatchers
> cannot deliver the same event twice — but it also means a process that dies
> mid-delivery would strand the event forever, so the retry service sweeps stale
> rows back.
>
> And `FAILED` is reachable on purpose. The brief asks to re-send when delivery
> *has definitely failed*, so something has to mean exactly that."

**Then the honest part:**

> "In my previous version, `FAILED` was unreachable — a failure went back to
> `PENDING`. And the replay endpoint guarded on the wrong states: it rejected
> `FAILED` and accepted `PENDING`. Two bugs that were consistent with each
> other, which is why neither was visible.
>
> Making the transitions an explicit table is what turns that class of error
> into a test failure instead of a silent behaviour."

---

## Slide 8 · Retry, and who owns the decision

> "There are three layers, and they answer different questions.
>
> In-process retries handle a transient blip — exponential backoff with full
> jitter. They are not persisted: they are transport noise, and a row per
> attempted TCP connection buries the audit trail.
>
> Asynchronous retries are a separate service, because *should we try again, and
> when* is a policy. The dispatcher only ever answers *did it work*. Fold them
> together and 'how many retries do we allow' gets three different answers
> across the codebase.
>
> And the manual replay is the client's decision, on an event the platform has
> given up on."

Two implementation details worth naming:

> "`FOR UPDATE SKIP LOCKED` plus a visibility window, so several retry instances
> get disjoint batches and no database transaction is held open across a Kafka
> publish. And full jitter — drawn from the whole window, not a wobble on top of
> it — because when a client's endpoint recovers, the hundred events waiting on
> it must not all fire at once and knock it back over."

---

## Slide 9 · The data model

`docs/diagrams/04-data-model.md`.

> "The event holds what the platform said. The attempt holds what we did about
> it. Keeping them apart is what lets a replay months later send exactly the
> original payload rather than a reconstruction."

On identifiers:

> "`id` is ours. `event_id` and `client_id` come from upstream, so they are
> opaque indexed strings. We do not control another system's identifier format —
> and the first sample of real data proved it: the fixture in the brief uses
> `EVT001`, which does not fit a UUID column. So it loads verbatim, with no
> mapping trick to explain."

---

## Slide 10 · Security

Three in depth. **Do not list all ten.**

**A01 — the one that was live.**
> "`client_id` came from the query string, and the detail endpoint never checked
> the owner. Two requests and one client reads another's payment history. Now
> identity comes from the token claim and ownership is part of the SQL `WHERE`,
> so it cannot be forgotten at a call site. A foreign id answers 404, not 403 —
> a 403 confirms the id exists."

**A10 — the one this design invites.**
> "The product feature is that a client tells us which URL to call. Point it at
> `169.254.169.254` and we fetch cloud credentials on their behalf, from inside
> the VPC, and store them where they can read them back.
>
> Guarded at registration, again before every delivery, and once more at the
> socket — because a hostname can be public today and `127.0.0.1` tomorrow.
> Only the socket-level check sees that."

> "A test I wrote for `::ffff:127.0.0.1` failed, and that is how I learned the
> URL parser normalises it to `::ffff:7f00:1` — which a string check waves
> straight through. It decodes from the address bits now."

**A08.**
> "HMAC-SHA256 with the timestamp inside the signed material, so a captured
> request is not replayable forever. Idempotency as a database constraint, not a
> code path. And a compare-and-set claim, so at-least-once *consumption* does
> not become at-least-once *delivery*."

Close with the gaps slide — six named limitations from `security-owasp.md`.

> "Those are scope decisions, not oversights, and I would rather name them than
> have you find them."

---

## Slide 11 · Observability

> "The brief asks for two things in one sentence: identify behaviour deviation,
> and respond to a client complaint. Those need different tools.
>
> Grafana answers the first. It cannot answer the second — a success rate of
> 98.4% tells you nothing about the one delivery someone is on the phone about.
>
> Both are fed by the same delivery-result topic, so they cannot disagree. And
> the monitor is a Kafka consumer with no database connection: switch it off and
> delivery does not notice."

---

## Slide 12 · Close

> "One pipeline, three origins, one audit trail. The producer knows nothing
> about webhooks; the delivery logic knows nothing about Kafka or PostgreSQL;
> and the linter keeps it that way.
>
> Let me show you."

→ demo

---

# Question bank

### "Why not just call the webhook from the payments service?"
Then a payment's latency depends on a stranger's uptime, and a client whose
endpoint is down slows down payments. Kafka turns a delivery outage into
consumer lag: visible, bounded, recoverable.

### "What delivery guarantee do you offer?"
At-least-once into the pipeline, effectively exactly-once out. Kafka redelivers
on failure; `event_id` is unique so ingestion is idempotent; and
`ClaimForDelivery` is a compare-and-set, so two dispatchers that both read the
same event produce one webhook call. The client should still treat the delivery
as at-least-once and use `notification_event_id` — that is what a correct
integration does regardless of our guarantees.

### "Why don't you persist every HTTP retry?"
Because an in-process retry is a TCP-level event, not a business one. A row per
attempted connection means a client looking for what happened to their event
scrolls through twenty rows that all say the same thing. The attempt count is in
the logs and metrics. If compliance required each one, it is a policy change in
the adapter — the domain does not move.

### "What if the webhook is slow?"
A hard timeout, bounded in-process attempts, then the event goes to the retry
service. The consumer is not blocked. What is missing today is a circuit breaker
per webhook host, so one slow client cannot consume dispatcher concurrency —
that is on the gap list.

### "How would you scale this?"
Add dispatcher instances in the consumer group; the topic has three partitions
and messages are keyed by `client_id`, so per-client order survives. The retry
service scales the same way — `SKIP LOCKED` means instances take disjoint
batches instead of contending. PostgreSQL is the first real ceiling:
`notification_attempts` grows fastest and would want time-based partitioning
with older partitions rolled to cold storage.

### "Why a database poller instead of Kafka delay topics?"
Delay topics are the right answer at high volume, and the migration is contained
to one service. Today the poller keeps the backoff curve in code rather than in
topology, keeps the state queryable by the same API that serves clients, and
reuses an index that already exists. Adding five consumers to a system that does
not need them yet is cost without benefit.

### "What happens if the dispatcher dies mid-delivery?"
The event is left in `DELIVERING`, where nothing else looks — the dispatcher
skips it by design, and the retry poller only reads `RETRYING`. So there is a
sweep: rows in `DELIVERING` older than a threshold go back to `RETRYING`. It is
the kind of thing that only shows up when you draw the state machine and ask
what happens at each node.

### "Why is `demo-tools` not hexagonal?"
Because it is three scripts and a mock endpoint. Adding ports and use cases
would add files without making anything easier to change. Knowing where the
structure does not pay is part of applying it — it is written down in ADR 3 so
it reads as a decision rather than an omission.

### "Isn't a five-state machine over-engineered for this?"
The states are not the engineering; they are the requirements. `FAILED` exists
because the brief asks to re-send when delivery has definitely failed.
`DELIVERING` exists because two instances must not deliver twice. `RETRYING`
exists because the retry policy lives in another service. Take any one away and
a requirement stops being expressible.

### "How do you know the linter rule actually works?"
I break it during the demo.

### "What would you do first with more time?"
A circuit breaker per webhook host, then automated dependency scanning, then
moving JWT verification to the platform's identity provider with asymmetric
keys. In that order, because the first is the one that can take the platform
down.

### "What are you least happy with?"
`hmac_secret` is stored in plaintext. It should be encrypted at rest with a
KMS-managed key. It is on the gap list, and it is the one I would fix before
this handled real money.

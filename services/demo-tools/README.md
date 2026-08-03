# demo-tools

Stands in for two things that are **not part of the product**: the platform
microservices that emit business events, and a client's webhook endpoint.

These are scripts. They are deliberately not hexagonal — applying the structure
here would be ceremony, and saying so is part of knowing when it pays
([ADR 0003](../../docs/adr/0003-hexagonal-boundaries.md)).

## Why it exists as its own service

The previous `subscription-service` did four unrelated jobs at once: it managed
subscriptions, resolved them for the dispatcher, **was** the client's webhook,
and **was** the Kafka producer. That is a mock wearing a service's name, and a
technical panel notices. Splitting the simulation out leaves the subscription
service doing one thing.

## Every action is available twice

Once as a CLI script, and once as an HTTP endpoint — because the demo is driven
from Postman, and switching to a terminal mid-presentation to run a shell
script is exactly the kind of seam that makes a demo feel improvised.

Both paths call the same functions in `src/simulate.ts`, so they cannot drift.

| Action | HTTP | CLI |
|---|---|---|
| Register the fixture's subscriptions | `POST /simulate/subscribe-all` | `make subscribe-all` |
| Deliver all ten fixture events | `POST /simulate/deliver-all` | `make deliver-all` |
| Publish one event | `POST /simulate/publish` | `pnpm run publish-event --client CLIENT001` |
| Load the fixture as history | `POST /simulate/seed` | `make seed` |
| Make the webhook fail | `POST /control` | `make fail-next N=20` |
| See what arrived | `GET /received` | — |

`POST /simulate/subscribe-all` takes a `webhook_url` in the body — that is what
you use on presentation day with the public HTTPS endpoint the panel provides.

### `deliver-all` and `seed` are alternatives, not a sequence

`deliver-all` sends the fixture through the live pipeline, so the ten events end
up `DELIVERED` with real attempts against a real webhook. That is the case
statement's Task 2, executed.

`seed` writes them as already-settled history — seven `DELIVERED`, three
`FAILED` — which is useful when you want the list filters and the replay
endpoint populated instantly.

Running `seed` first makes `deliver-all` a no-op, and correctly so: those events
are already in terminal states, and the dispatcher refuses to deliver an event
twice. Use `make reset-events` to switch between the two.

## The webhook receiver

The previous mock failed on a coin flip — 50% of deliveries returned 500. That
made the retry path impossible to demonstrate reliably and the happy path
unreliable too; the old runbook literally advised publishing another event and
trying again if it failed four times in a row.

Here failure is asked for:

```bash
make fail-next N=20        # the next 20 deliveries return 500
make webhook-ok            # back to succeeding

curl -X POST localhost:3004/control -d '{"status":503}'   # every delivery fails
curl -X POST localhost:3004/control -d '{"delayMs":6000}' # every delivery hangs
```

Inspect what arrived:

```bash
curl localhost:3004/received | jq
```

Each record shows the traceability headers and, crucially, `dispatch_source` —
so a first delivery, an automatic retry and a client replay are visibly the
same request shape arriving by the same route.

### Signature verification

`subscribe-all` hands the receiver the signing secrets the subscription service
issued, keyed by `client_id` and `event_type` — which is how a real client
stores them, since each subscription gets its own.

`verifySignature` in `src/webhook-server.ts` is therefore the **reference
implementation a client would write**: it recomputes HMAC-SHA256 over
`<timestamp>.<raw body>`, rejects anything older than five minutes, and compares
in constant time. The raw body is used deliberately — re-serialising parsed JSON
changes key order and the signature stops matching, which is the single most
common integration bug on the client side.

Received deliveries report `valid`, `invalid`, `stale`, `missing` or
`unverified`.

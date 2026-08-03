# subscription-service

Owns the delivery contract: which of a client's event types goes to which
webhook, over which method, and what response counts as accepted.

Node + Express, hexagonal.

## Why this service is the tenant boundary

The case statement puts one requirement in bold terms:

> *"It's mandatory to ensure notifications sent to every client belong to
> events generated to that client."*

That is enforced here. Resolution is keyed on `(client_id, event_type)`
together, never on `event_type` alone, and the database has a unique constraint
on the pair. An event can only ever reach a webhook its own client registered.

## Endpoints

| | |
|---|---|
| `POST /subscriptions` | register one or more event types against a webhook |
| `GET /subscriptions` | list the caller's own subscriptions |
| `DELETE /subscriptions/:id` | deactivate one |
| `GET /internal/subscriptions/resolve` | **internal only** — what the dispatcher calls |
| `POST /auth/token` | demo token issuer, behind `ENABLE_DEMO_TOKENS` |

`/internal/*` is never exposed at the edge. It returns the signing secret and
it takes a `client_id` as a parameter — precisely the shape that must not be
reachable from the internet.

## Security decisions

**`client_id` comes from the token.** Always, everywhere, with no fallback to
the query string or the body. This is the single change that closes the
cross-tenant read the previous implementation had.

**JWT with the algorithm pinned.** `algorithms: ['HS256']` plus issuer and
audience checks. Without pinning, the library trusts an attacker-controlled
header to decide how to verify — the `alg: none` and RS256→HS256 forgeries.

**Ownership failures answer 404.** A 403 confirms the resource exists, which
turns the endpoint into an enumeration oracle.

**A signing secret per subscription**, returned exactly once at creation. It is
never readable again — not in the list response, not anywhere. The dispatcher
reads it over the internal route to sign outgoing webhooks.

**The SSRF guard** (`src/infrastructure/security/webhook-url-guard.ts`) is the
substantial one, because this endpoint lets a client tell the platform which URL
to call. It rejects non-HTTPS, credentials in the URL, well-known service ports,
and any hostname that resolves into loopback, private, link-local (including
`169.254.169.254`), CGNAT or multicast space — checking **every** DNS answer,
since which one the dialer picks is not ours to predict. IPv4-mapped IPv6 is
decoded from the bits rather than pattern-matched on the string: a test for
`::ffff:127.0.0.1` is what caught that WHATWG normalises it to `::ffff:7f00:1`
and a regex-based check waves it straight through.

Registration is only the first layer. The dispatcher revalidates before every
call and again at socket level, because a hostname that resolves publicly today
can resolve to `127.0.0.1` tomorrow.

**Deactivate, never delete.** A delivery that failed last week has to stay
explainable next month, and its audit trail references this row.

## Running the tests

```bash
pnpm test        # 48 cases: the SSRF rules, and the API including tenant isolation
pnpm typecheck
pnpm lint        # includes the dependency rule
```

## Configuration

| Variable | Default | |
|---|---|---|
| `DATABASE_URL` | — | required |
| `JWT_SECRET` | — | required, minimum 16 characters |
| `JWT_ISSUER` | `notifications-platform` | |
| `JWT_AUDIENCE` | `notifications-api` | |
| `SUBSCRIPTION_SERVICE_PORT` | `3001` | |
| `ENABLE_DEMO_TOKENS` | `false` | mounts `POST /auth/token` |
| `WEBHOOK_REQUIRE_HTTPS` | `true` | **relaxing this is logged as a warning** |
| `WEBHOOK_ALLOW_PRIVATE_NETWORKS` | `false` | **relaxing this is logged as a warning** |

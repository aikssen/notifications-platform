# Security — OWASP Top 10, mapped to the code that mitigates it

> *"The Cobre API could be consumed publicly through the internet, then identify
> at least three OWASP Top 10 vulnerabilities that could impact your API.
> Propose security measures to mitigate each one of your choices."*

Three are treated in depth, because they are the three this system actually
exposes: **A01 Broken Access Control**, **A10 Server-Side Request Forgery**, and
**A08 Software and Data Integrity Failures**. The rest are covered afterwards.

Every control below points at a file and a line. Nothing here is a proposal.

---

## A01 · Broken Access Control — *the one that was live*

### The exposure

A multi-tenant API where any authenticated caller can read another client's
data. In the previous implementation this was not theoretical:

```ts
const clientId = req.query.client_id as string;   // the caller decides who they are
const result = await getNotificationEventDetail.execute(rawId);  // owner never checked
```

Two requests, and one client reads another's payment payloads and delivery
history. The identity came from the request, and the detail endpoint did not
filter by it at all.

### The mitigations

**Identity comes from the verified token, and only from there.**
`services/self-service-api/src/infrastructure/http/auth.ts:58` — `clientId` is
the JWT `sub` claim. There is no fallback to the query string or the body. Every
handler calls `clientIdOf(req)`, which throws rather than defaulting.

**Ownership is part of the lookup, not a check applied afterwards.**
`postgres-event-repository.ts:98` — `WHERE id = $1 AND client_id = $2`. The
repository method has no signature that lets a caller forget the owner. Same for
replay at line `126`, which additionally guards on `state = 'FAILED'`.

**A foreign resource answers 404, never 403.**
`routes.ts:90`. A 403 confirms the id exists, which turns the endpoint into an
enumeration oracle.

**Tenant isolation is enforced again at the delivery layer.** Subscription
resolution is keyed on `(client_id, event_type)` together —
`subscription-repository.ts:55` — and the dispatcher refuses an answer that
comes back for a different client:
`services/dispatch-service/internal/infrastructure/subscriptions/resolver.go:92`.
That is defence in depth: even a compromised subscription service cannot cause a
cross-tenant delivery.

**Internal endpoints are not on the public surface.**
`subscription-service/src/infrastructure/http/routes.ts:100` — the resolve
endpoint returns a signing secret and takes a `client_id` parameter, which is
exactly the shape that must never be reachable from the internet. It is mounted
under `/internal` and reachable only on the private network.

### Proof

`services/self-service-api/src/infrastructure/http/app.test.ts` — a client
cannot list, read or replay another's events. Against the running stack:

```
GET   /notification_events/{other client's id}  as CLIENT001 -> 404
POST  /notification_events/{other client's id}/replay        -> 404
GET   same id as its owner CLIENT002                         -> 200
```

---

## A10 · Server-Side Request Forgery — *the one this design invites*

### The exposure

This platform's core feature is that a client tells it which URL to call. That
is a request-forgery primitive offered as a product.

Point a subscription at `http://169.254.169.254/latest/meta-data/iam/` and the
platform fetches cloud credentials on the attacker's behalf, from inside the
VPC, and stores the response in `notification_attempts.response_body` where they
can read it back through the self-service API.

### The mitigations

Two layers, in two services.

**At registration** —
`services/subscription-service/src/infrastructure/security/webhook-url-guard.ts:50`.
HTTPS only. No credentials in the URL. Well-known service ports refused (`:22`,
`:5432`, `:6379`, `:9092`, …) plus privileged ports generally. The host is
resolved and **every** answer checked, because which one the dialer picks is not
ours to predict. Blocked ranges: loopback, RFC 1918 private, link-local
(including `169.254.169.254`), CGNAT, multicast, reserved, and their IPv6
equivalents.

**At delivery, twice** —
`services/dispatch-service/internal/infrastructure/webhook/ssrf.go:76` revalidates
the URL before every call, and `ssrf.go:194` is installed as the socket dialer's
`Control` function, so the check also runs against the address the connection is
actually opening to.

That second one is the one that matters. A hostname can resolve to a public
address when the subscription is created and to `127.0.0.1` an hour later — DNS
rebinding. Validating the URL alone never sees it; validating at dial time does.

**Redirects are not followed.**
`services/dispatch-service/internal/infrastructure/webhook/sender.go:69` — a 302
to an internal address is the simplest way around any URL allowlist, and a
webhook has no legitimate reason to redirect.

**An SSRF rejection is never retried.** `sender.go` treats it as permanent;
retrying only amplifies the probe.

### A real bypass, found by a test

Writing a case for `::ffff:127.0.0.1` failed. WHATWG URL parsing normalises that
to `::ffff:7f00:1`, so a string-matching check waves it straight through.
IPv4-mapped addresses are now decoded from the address bits
(`webhook-url-guard.ts:143` and below), not pattern-matched on text.

### Relaxation is explicit and loud

The local demo needs plain HTTP on a private network. Rather than weakening the
validator, two named switches exist — `WEBHOOK_REQUIRE_HTTPS` and
`WEBHOOK_ALLOW_PRIVATE_NETWORKS` — and both services log a warning at startup
when either is set:

```
WARN  SSRF protections are relaxed — acceptable for a local demo, never for production
```

Set them to their production values and `make subscribe-all` is refused with
`SCHEME_NOT_ALLOWED`. That refusal is worth demonstrating.

---

## A08 · Software and Data Integrity Failures

### The exposure

Two distinct problems. A client cannot tell a genuine delivery from a forged
one — anyone who learns the webhook URL can post fabricated payment events to
it. And a duplicated Kafka message could produce a duplicate notification, which
in a financial context means a client acting twice on one payment.

### The mitigations

**Every delivery is signed.**
`services/dispatch-service/internal/infrastructure/webhook/signature.go:29` —
HMAC-SHA256 over `<unix timestamp> "." <raw body>`, sent as `X-Signature` with
`X-Timestamp`, using a secret unique to that subscription.

The timestamp is *inside* the signed material deliberately. Signing the body
alone produces a token that stays valid forever, so a captured request is
replayable indefinitely. Binding the timestamp lets the receiver reject anything
outside a short window — `demo-tools/src/webhook-server.ts` enforces five
minutes and compares in constant time, and doubles as the reference
implementation for a client.

**Secrets are shown once.** Returned at subscription creation and never
readable again — not in the list response, not anywhere. The dispatcher reads
them over the internal route.

**Idempotency is a database constraint, not a code path.**
`event_store.go:80` — `ON CONFLICT (event_id) DO NOTHING`, backed by
`UNIQUE (event_id)`. Two dispatchers ingesting the same event concurrently is
handled explicitly: the loser re-reads the winner's row rather than continuing
with an id that was never persisted.

**At-least-once consumption does not become at-least-once delivery.**
`event_store.go:97` — `ClaimForDelivery` is a compare-and-set on `state`. Two
dispatchers can read the same `PENDING` event; only one `UPDATE` matches, and
only that one calls the webhook.

**The attempt and the state change are one transaction.** Splitting them is how
an audit trail starts disagreeing with the state it is meant to explain.

**Message schemas are validated at every boundary** — `zod` on both Node APIs
(`routes.ts:22` and `routes.ts:19`, both `.strict()`, so an unknown field is a
rejection), and explicit decoding with required-field checks on the Kafka
consumer.

---

## The rest of the Top 10

| | Status | Where |
|---|---|---|
| **A02 Cryptographic Failures** | Mitigated | HTTPS enforced on webhooks; HMAC-SHA256 with per-subscription secrets; JWT `HS256` with the algorithm pinned; secrets injected at runtime, never committed; `DATABASE_URL` never logged — a connection string is a password |
| **A03 Injection** | Mitigated | Every query parameterised, including the dynamically-assembled filter clause, which builds from a fixed set of fragments and a positional parameter list; `zod` schemas at both APIs; `CHECK` constraints as a last line |
| **A04 Insecure Design** | Mitigated | A bounded retry budget with a dead letter queue rather than unbounded retries; rate limiting; response bodies capped at 64 KB so a hostile webhook cannot exhaust the dispatcher; page size capped so one request cannot pull the whole table |
| **A05 Security Misconfiguration** | Mitigated | `helmet` (`app.ts:34`); `x-powered-by` disabled; stack traces logged but never returned; distroless non-root images with no shell; Kafka topic auto-creation off; secure defaults with relaxations that warn |
| **A06 Vulnerable and Outdated Components** | Partial | Dependencies pinned via lockfiles; minimal surface — the Go images contain a static binary and CA certificates. **Gap:** no automated dependency scanning in CI |
| **A07 Identification and Authentication Failures** | Mitigated | JWT with `algorithms: ['HS256']` pinned (`auth.ts:43`) — without pinning, an attacker-controlled header decides how the token is verified, which is the `alg: none` and RS256→HS256 forgery; issuer and audience validated; expiry enforced; failures do not say why |
| **A09 Security Logging and Monitoring Failures** | Mitigated | Structured JSON logs with a correlation id that follows a request across services; `authorization` redacted (`app.ts:46`); Prometheus metrics on all five services; a provisioned Grafana board; a live operations dashboard |

## Honest gaps

Named rather than glossed over. Each is a scope decision, not an oversight.

1. **Tokens are symmetric and issued by the service itself.** Production would
   use the platform's identity provider with asymmetric keys and JWKS rotation.
   The middleware is where that changes; nothing else moves.
2. **No automated dependency or container scanning in CI.**
3. **Secrets are in environment variables**, not a secret manager with rotation.
4. **`hmac_secret` is stored in plaintext** in the subscriptions table. It should
   be encrypted at rest with a KMS-managed key.
5. **No per-client rate limiting on outbound deliveries.** A client with a slow
   endpoint can consume dispatcher concurrency. A circuit breaker per webhook
   host is the fix.
6. **The `/internal` route is network-isolated but not authenticated.** Adding
   mTLS or a service token is the right hardening for a zero-trust network.

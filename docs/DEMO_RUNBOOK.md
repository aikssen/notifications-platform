# Demo runbook

Every step here has been run against the live stack. Nothing is random, and
there is no step that says "if it fails, try again".

**Total: about 12 minutes.** Sections marked *optional* are for questions.

## Run it from Postman

Import `docs/api/notifications.postman_collection.json`. Its folders are
numbered in the order below, and **the whole demo runs from there** — including
publishing events and making the client webhook fail, which are HTTP endpoints
on `demo-tools` rather than shell scripts.

| Runbook section | Postman folder |
|---|---|
| 2 · Subscriptions and SSRF | `0 · Setup`, `3 · Subscriptions and the SSRF guard` |
| 3 · Deliver the fixture | `0 · Setup / Deliver the whole fixture` |
| 4 · Failure and retry | `2 · Failure, retry and exhaustion` |
| 5 · Self-service API | `1 · Notification events` |
| 6 · Tenant isolation | `4 · Access control` |

The `curl` commands below are the same calls, kept for anyone who prefers a
terminal and for the two steps that genuinely need one — swapping the
repository adapter and breaking the dependency rule.

On presentation day, set the collection's `webhook_url` variable to the public
HTTPS URL the panel provides, then run `0 · Setup` top to bottom.

---

## Before the room

```bash
cd notifications-platform
cp .env.example .env
make up            # first run pulls images and builds — do this beforehand
make ps            # everything Up, postgres and kafka healthy
```

Open four tabs:

| | |
|---|---|
| Operations dashboard | http://localhost:3003 |
| Grafana | http://localhost:3000/d/notifications-delivery |
| A terminal | for `make` and `curl` |
| An editor | on `services/dispatch-service/internal/application/port/ports.go` |

Reset to a clean state:

```bash
make reset-events
curl -s -X POST localhost:3004/received/reset
make webhook-ok
```

### On presentation day

The panel provides a public HTTPS URL. Use it:

```bash
# in .env — the production values, since the endpoint is real HTTPS
WEBHOOK_REQUIRE_HTTPS=true
WEBHOOK_ALLOW_PRIVATE_NETWORKS=false

make up
WEBHOOK_URL=https://the-url-they-give-you/hook make subscribe-all
make deliver-all
```

---

## 1 · The fixture is their own data (1 min)

```bash
cat fixtures/notification_events.json | head -20
```

> "This is the file attached to the case statement, unmodified. Ten events,
> three clients, and three of them recorded as `failed`. Those three are not
> incidental — they are what the replay requirement acts on."

---

## 2 · Subscriptions, and the SSRF guard (2 min)

```bash
make subscribe-all
```

> "Ten subscriptions, registered through the public API. Each one gets its own
> signing secret, returned exactly once."

Then show the guard refusing:

```bash
TOKEN=$(curl -s -X POST localhost:3001/auth/token \
  -H 'content-type: application/json' -d '{"client_id":"CLIENT001"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["access_token"])')

curl -s -X POST localhost:3001/subscriptions \
  -H "authorization: Bearer $TOKEN" -H 'content-type: application/json' \
  -d '{"webhook_url":"https://169.254.169.254/latest/meta-data/","events":["x"]}' | jq
```

```json
{ "error": "webhook_url_rejected", "code": "ADDRESS_BLOCKED",
  "message": "webhook_url resolves to 169.254.169.254, which is link-local (cloud metadata)" }
```

> "The feature of this platform is that a client tells us which URL to call.
> That is a request-forgery primitive offered as a product, so it is guarded at
> registration, again before every delivery, and once more at the socket — DNS
> rebinding means a hostname can be public today and `127.0.0.1` tomorrow."

---

## 3 · Deliver the fixture through the real pipeline (2 min)

Put the dashboard on screen first.

Postman: `0 · Setup / Deliver the whole fixture` (or `make deliver-all`).

Watch ten rows arrive. Point at the `ORIGIN` column: all `SYSTEM`.

```bash
curl -s localhost:3004/received | jq '.received[0]'
```

> "Signature `valid`. Every delivery is HMAC-signed with that subscription's own
> secret, over the timestamp and the raw body — so a captured request cannot be
> replayed forever."

That is Task 2: the attached file, obtained from a repository, delivered by
webhook.

---

## 4 · Failure, retry, exhaustion (3 min)

Postman: `2 · Failure, retry and exhaustion`, top to bottom.

```bash
# the same two calls, if you prefer curl
curl -X POST localhost:3004/control -H 'content-type: application/json' -d '{"failNext":50}'
curl -X POST localhost:3004/simulate/publish -H 'content-type: application/json' \
  -d '{"client_id":"CLIENT001","event_type":"credit_card_payment","event_id":"EVT-DEMO"}'
```

The retry settings in `.env` are tuned for the demo: the budget is spent in
roughly 45 seconds. Production values are in the comment beside them — eight
attempts spread over several hours, so a client's endpoint has time to actually
come back before the platform gives up on it.

On the dashboard, in order:

1. `SYSTEM` · attempt 1 · HTTP 500 · `RETRYING`
2. `RETRY_SERVICE` · attempt 2 — the retry worker requeued it
3. attempts 3, 4, 5, with the gap between them growing
4. `FAILED`

> "Three things happened. The dispatcher retried in place with exponential
> backoff and full jitter — those attempts are not persisted, they are transport
> noise. Then it handed the event to the retry service, which owns the policy:
> the dispatcher only ever answers *did it work*. And when the budget ran out,
> the retry service wrote `FAILED` — which is the state the replay endpoint acts
> on."

```bash
docker exec notif-platform-postgres psql -U notifications -d notifications -c \
 "SELECT attempt_number, dispatch_source, status, response_status
  FROM notification_attempts na JOIN notification_events ne ON ne.id=na.notification_event_id
  WHERE ne.event_id='EVT-DEMO' ORDER BY attempt_number"
```

Optional — the dead letter record:

```bash
docker exec notif-platform-kafka kafka-console-consumer \
  --bootstrap-server localhost:9092 --topic notifications.dlq \
  --from-beginning --max-messages 1 --timeout-ms 5000 | jq
```

---

## 5 · The self-service API (2 min)

Postman: `1 · Notification events`.

```bash
# the same calls, if you prefer curl
curl -X POST localhost:3004/control -H 'content-type: application/json' -d '{"reset":true}'

TOKEN=$(curl -s -X POST localhost:3002/auth/token \
  -H 'content-type: application/json' -d '{"client_id":"CLIENT001"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["access_token"])')

# both filters the brief asks for
curl -s "localhost:3002/notification_events?delivery_status=FAILED&created_from=2024-01-01T00:00:00Z" \
  -H "authorization: Bearer $TOKEN" | jq '.notification_events[], .pagination'
```

**The replay — the requirement that was broken:**

```bash
ID=$(curl -s "localhost:3002/notification_events?delivery_status=FAILED" \
  -H "authorization: Bearer $TOKEN" \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["notification_events"][0]["notification_event_id"])')

curl -s -X POST "localhost:3002/notification_events/$ID/replay" \
  -H "authorization: Bearer $TOKEN" | jq
```

Switch to the dashboard: one new row, origin `SELF_SERVICE`, state `DELIVERED`.

```bash
docker exec notif-platform-postgres psql -U notifications -d notifications -c \
 "SELECT attempt_number, dispatch_source, status FROM notification_attempts na
  JOIN notification_events ne ON ne.id=na.notification_event_id
  WHERE ne.event_id='EVT-DEMO' ORDER BY attempt_number"
```

> "Six attempts. One from the system, four automatic retries, one client replay
> — and all six travelled the identical code path. They differ in one field,
> which exists so this table can tell them apart. There is no second
> implementation of delivery that could drift from the first."

**This is the strongest moment of the demo. Pause on that table.**

---

## 6 · Tenant isolation, live (1 min)

```bash
TOKEN_B=$(curl -s -X POST localhost:3002/auth/token \
  -H 'content-type: application/json' -d '{"client_id":"CLIENT002"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["access_token"])')

curl -s -o /dev/null -w "CLIENT002 reading CLIENT001's event -> %{http_code}\n" \
  "localhost:3002/notification_events/$ID" -H "authorization: Bearer $TOKEN_B"
```

`404`.

> "Not 403 — a 403 confirms the id exists. And the client identity comes from
> the token claim, never from a query parameter, so there is no parameter to
> edit."

---

## 7 · The hexagonal boundary, proven (1 min)

Two demonstrations, both about ten seconds.

**Swap the repository adapter:**

```bash
EVENTS_REPOSITORY=file docker compose -f deploy/docker-compose.yml --env-file .env up -d self-service-api
sleep 6
curl -s localhost:3002/healthz | jq          # { "repository": "file" }
curl -s "localhost:3002/notification_events?delivery_status=FAILED" \
  -H "authorization: Bearer $TOKEN_B" | jq '.notification_events'
```

> "Same endpoint, same contract, now served from the JSON file — through the
> same port. Neither use case can tell which adapter it received."

```bash
docker compose -f deploy/docker-compose.yml --env-file .env up -d self-service-api   # back
```

**Break the dependency rule:**

```bash
cd services/dispatch-service
sed -i '' 's|^import "fmt"$|import (\n\t"fmt"\n\t_ "github.com/jackc/pgx/v5"\n)|' internal/domain/values.go
golangci-lint run ./internal/domain/...
git checkout internal/domain/values.go
cd ../..
```

```
internal/domain/values.go:6:2: import 'github.com/jackc/pgx/v5' is not allowed from list 'domain' (depguard)
```

> "The dependency rule is not a convention in a README. It fails the build."

---

## 8 · Observability (1 min)

Grafana: success rate, throughput, latency percentiles, outcomes by origin, the
retry backlog age.

> "Grafana answers *is the platform deviating from normal*. It cannot answer
> *what happened to this client's event* — a success rate of 98.4% says nothing
> about the one delivery someone is calling about. That is the other dashboard,
> and both are fed by the same delivery-result topic, so they cannot disagree.
>
> The monitor is a Kafka consumer and nothing else. It has no database
> connection, and the delivery path has no reference to it. Switch it off and
> delivery does not notice."

---

## If something goes wrong

| Symptom | Do this |
|---|---|
| A service is not up | `make ps`, then `make logs S=<service>` |
| Nothing arrives after `deliver-all` | The events are already terminal. `make reset-events`, then republish |
| The webhook keeps failing | `make webhook-ok` |
| Subscription returns 409 | It already exists — that is correct, move on |
| Port already in use | `lsof -nP -iTCP:3001 -sTCP:LISTEN` |
| Total loss | `make reset && make up` — about 90 seconds |

**Do not run `make reset` immediately before presenting.** It wipes the volumes
and everything needs rebuilding.

## Afterwards

```bash
make down      # keeps the data
```

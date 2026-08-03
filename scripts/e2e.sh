#!/usr/bin/env bash
#
# End-to-end check against a running stack.
#
# Unit tests prove the rules; this proves the wiring. It walks the same path the
# demo does and asserts on the result, so a broken deployment fails here rather
# than in front of a panel.
#
#   make up && ./scripts/e2e.sh
#
set -uo pipefail

SUBS=${SUBS:-http://localhost:3001}
SELF=${SELF:-http://localhost:3002}
MONITOR=${MONITOR:-http://localhost:3003}
HOOK=${HOOK:-http://localhost:3004}
PSQL=(docker exec notif-platform-postgres psql -U notifications -d notifications -tAc)

passed=0
failed=0

pass() { printf '  \033[32m✓\033[0m %s\n' "$1"; passed=$((passed + 1)); }
fail() { printf '  \033[31m✗\033[0m %s\n     %s\n' "$1" "${2:-}"; failed=$((failed + 1)); }
step() { printf '\n\033[1m%s\033[0m\n' "$1"; }

expect_status() {
  local want="$1" desc="$2"; shift 2
  local got; got=$(curl -s -o /dev/null -w '%{http_code}' "$@")
  [[ "$got" == "$want" ]] && pass "$desc" || fail "$desc" "expected $want, got $got"
}

json() { python3 -c "import sys,json;d=json.load(sys.stdin);print($1)" 2>/dev/null; }

token_for() {
  curl -s -X POST "$SELF/auth/token" -H 'content-type: application/json' \
    -d "{\"client_id\":\"$1\"}" | json 'd["access_token"]'
}

# ---------------------------------------------------------------------------
step 'Services are reachable'
for pair in "$SUBS subscription-service" "$SELF self-service-api" "$MONITOR monitor-service" "$HOOK demo-tools"; do
  set -- $pair
  expect_status 200 "$2 is healthy" "$1/healthz"
done
for pair in "9101 dispatch-service" "9102 retry-service"; do
  set -- $pair
  expect_status 200 "$2 is ready" "http://localhost:$1/readyz"
done

# ---------------------------------------------------------------------------
step 'Clean slate'
"${PSQL[@]}" "TRUNCATE notification_attempts, notification_events CASCADE; DELETE FROM subscriptions;" >/dev/null 2>&1 \
  && pass 'notification data and subscriptions cleared' || fail 'could not clear state'
curl -s -X POST "$HOOK/received/reset" >/dev/null
curl -s -X POST "$HOOK/control" -H 'content-type: application/json' -d '{"reset":true}' >/dev/null
pass 'demo webhook reset to succeeding'

# ---------------------------------------------------------------------------
step 'A10 — the SSRF guard'
TOKEN_A=$(token_for CLIENT001)
[[ -n "$TOKEN_A" ]] && pass 'obtained a token' || fail 'could not obtain a token'

probe() {
  local target="$1" suffix="$2"
  curl -s -o /dev/null -w '%{http_code}' -X POST "$SUBS/subscriptions" \
    -H "authorization: Bearer $TOKEN_A" -H 'content-type: application/json' \
    -d "{\"webhook_url\":\"$target\",\"events\":[\"ssrf_probe_$suffix\"]}"
}

# Service ports are refused in every mode: they are never a webhook endpoint.
for pair in '5432 postgres' '6379 redis' '22 ssh'; do
  set -- $pair
  code=$(probe "https://client.example.com:$1/hook" "port$1")
  [[ "$code" == "400" ]] && pass "refused port $1 ($2)" || fail "accepted port $1" "got $code"
done

# Address-range blocking depends on the deployment's stance. The local demo
# runs the client webhook on a private network, so the switches are relaxed —
# deliberately, and both services warn about it at startup.
if [[ "${WEBHOOK_ALLOW_PRIVATE_NETWORKS:-true}" == "true" ]]; then
  printf '  \033[33m—\033[0m address-range blocking not asserted: WEBHOOK_ALLOW_PRIVATE_NETWORKS=true\n'
  printf '     (unit tests cover it; set it to false to assert it here)\n'
else
  for target in \
    'https://169.254.169.254/latest/meta-data/' \
    'https://127.0.0.1/hook' \
    'https://10.0.0.1/hook'
  do
    code=$(probe "$target" "addr")
    [[ "$code" == "400" ]] && pass "refused $target" || fail "accepted $target" "got $code"
  done
fi

# Whatever the mode, the probes must not have left subscriptions behind.
"${PSQL[@]}" "DELETE FROM subscriptions WHERE event_type LIKE 'ssrf_probe%'" >/dev/null 2>&1

# ---------------------------------------------------------------------------
step 'A01 — authentication is required'
expect_status 401 'listing without a token is refused' "$SELF/notification_events"
expect_status 401 'a forged token is refused' "$SELF/notification_events" \
  -H 'authorization: Bearer not.a.token'

# ---------------------------------------------------------------------------
step 'Subscriptions and delivery'
( cd services/demo-tools && \
  SUBSCRIPTIONS_BASE_URL=$SUBS WEBHOOK_CONTROL_URL=$HOOK \
  WEBHOOK_URL=http://demo-tools:3004/webhook \
  FIXTURE_PATH=../../fixtures/notification_events.json \
  pnpm run subscribe-all ) >/dev/null 2>&1
subs=$("${PSQL[@]}" "SELECT count(*) FROM subscriptions WHERE status='ACTIVE'")
[[ "$subs" == "10" ]] && pass "10 subscriptions registered" || fail 'subscriptions' "got $subs"

( cd services/demo-tools && \
  KAFKA_BROKERS=localhost:9092 FIXTURE_PATH=../../fixtures/notification_events.json \
  pnpm run deliver-all ) >/dev/null 2>&1

for _ in $(seq 1 20); do
  delivered=$("${PSQL[@]}" "SELECT count(*) FROM notification_events WHERE state='DELIVERED'")
  [[ "$delivered" == "10" ]] && break
  sleep 1
done
[[ "$delivered" == "10" ]] && pass 'all 10 fixture events delivered' || fail 'delivery' "only $delivered"

attempts=$("${PSQL[@]}" "SELECT count(*) FROM notification_attempts WHERE status='SUCCESS'")
[[ "$attempts" == "10" ]] && pass '10 successful attempts recorded' || fail 'audit trail' "got $attempts"

signed=$(curl -s "$HOOK/received" | json 'sum(1 for r in d["received"] if r["signature"]=="valid")')
[[ "$signed" == "10" ]] && pass 'every delivery carried a valid signature' || fail 'signatures' "got $signed"

# ---------------------------------------------------------------------------
step 'Filters required by the brief'
listed=$(curl -s "$SELF/notification_events?delivery_status=DELIVERED" \
  -H "authorization: Bearer $TOKEN_A" | json 'd["pagination"]["total"]')
[[ "$listed" == "4" ]] && pass 'filter by delivery_status' || fail 'delivery_status filter' "got $listed"

# deliver-all creates events now, so the range is asserted in both directions
# rather than against the fixture's own delivery_date (which `make seed` uses).
past=$(curl -s "$SELF/notification_events?created_from=2020-01-01T00:00:00Z" \
  -H "authorization: Bearer $TOKEN_A" | json 'd["pagination"]["total"]')
[[ "$past" == "4" ]] && pass 'created_from includes everything since 2020' \
  || fail 'created_from lower bound' "got $past"

future=$(curl -s "$SELF/notification_events?created_from=2099-01-01T00:00:00Z" \
  -H "authorization: Bearer $TOKEN_A" | json 'd["pagination"]["total"]')
[[ "$future" == "0" ]] && pass 'created_from excludes everything before it' \
  || fail 'created_from upper bound' "got $future"

bounded=$(curl -s "$SELF/notification_events?created_to=2020-01-01T00:00:00Z" \
  -H "authorization: Bearer $TOKEN_A" | json 'd["pagination"]["total"]')
[[ "$bounded" == "0" ]] && pass 'created_to bounds the range' || fail 'created_to' "got $bounded"

# ---------------------------------------------------------------------------
step 'A01 — one client cannot reach another'
other=$("${PSQL[@]}" "SELECT id FROM notification_events WHERE client_id='CLIENT002' LIMIT 1")
expect_status 404 "another client's event reads as 404" \
  "$SELF/notification_events/$other" -H "authorization: Bearer $TOKEN_A"
expect_status 404 "another client's event cannot be replayed" \
  -X POST "$SELF/notification_events/$other/replay" -H "authorization: Bearer $TOKEN_A"

TOKEN_B=$(token_for CLIENT002)
expect_status 200 'its own client can read it' \
  "$SELF/notification_events/$other" -H "authorization: Bearer $TOKEN_B"

# ---------------------------------------------------------------------------
step 'Failure, retry and exhaustion'
# Enough failures to spend the whole retry budget without the webhook ever
# recovering mid-way — which is what the first version of this script got
# wrong, resetting the endpoint before the budget was spent.
curl -s -X POST "$HOOK/control" -H 'content-type: application/json' -d '{"failNext":200}' >/dev/null

( cd services/demo-tools && KAFKA_BROKERS=localhost:9092 \
  pnpm exec tsx src/publish.ts --client CLIENT001 --type credit_card_payment \
  --event-id EVT-E2E ) >/dev/null 2>&1

for _ in $(seq 1 30); do
  state=$("${PSQL[@]}" "SELECT state FROM notification_events WHERE event_id='EVT-E2E'")
  [[ "$state" == "RETRYING" || "$state" == "FAILED" ]] && break
  sleep 1
done
[[ "$state" == "RETRYING" || "$state" == "FAILED" ]] \
  && pass "a failed delivery goes to $state, not PENDING" \
  || fail 'failed delivery state' "got '$state'"

# The retry worker has to pick it up and requeue it through the same topic.
for _ in $(seq 1 60); do
  retried=$("${PSQL[@]}" "SELECT count(*) FROM notification_attempts na
    JOIN notification_events ne ON ne.id=na.notification_event_id
    WHERE ne.event_id='EVT-E2E' AND na.dispatch_source='RETRY_SERVICE'")
  [[ "${retried:-0}" -ge 1 ]] && break
  sleep 2
done
[[ "${retried:-0}" -ge 1 ]] && pass "the retry service requeued it ($retried attempts so far)" \
  || fail 'no automatic retry observed' 'is retry-service running?'

# Then the budget runs out and the event becomes definitively failed.
for _ in $(seq 1 90); do
  state=$("${PSQL[@]}" "SELECT state FROM notification_events WHERE event_id='EVT-E2E'")
  [[ "$state" == "FAILED" ]] && break
  sleep 2
done
[[ "$state" == "FAILED" ]] && pass 'the retry budget was exhausted to FAILED' \
  || fail 'the retry budget never exhausted' "state is '$state' — RETRY_BASE_DELAY_SECONDS may be large"

# ---------------------------------------------------------------------------
step 'Replay — the requirement that was broken'
# Only now does the endpoint start succeeding, so the replay has something to
# succeed against.
curl -s -X POST "$HOOK/control" -H 'content-type: application/json' -d '{"reset":true}' >/dev/null

if [[ "$state" == "FAILED" ]]; then
  id=$("${PSQL[@]}" "SELECT id FROM notification_events WHERE event_id='EVT-E2E'")
  expect_status 202 'a definitively failed event can be replayed' \
    -X POST "$SELF/notification_events/$id/replay" -H "authorization: Bearer $TOKEN_A"

  for _ in $(seq 1 30); do
    replays=$("${PSQL[@]}" "SELECT count(*) FROM notification_attempts na
      JOIN notification_events ne ON ne.id=na.notification_event_id
      WHERE ne.event_id='EVT-E2E' AND na.dispatch_source='SELF_SERVICE'")
    [[ "${replays:-0}" -ge 1 ]] && break
    sleep 1
  done
  [[ "${replays:-0}" -ge 1 ]] \
    && pass 'the replay travelled the same pipeline, tagged SELF_SERVICE' \
    || fail 'replay never reached the dispatcher'

  for _ in $(seq 1 20); do
    final=$("${PSQL[@]}" "SELECT state FROM notification_events WHERE event_id='EVT-E2E'")
    [[ "$final" == "DELIVERED" ]] && break
    sleep 1
  done
  [[ "$final" == "DELIVERED" ]] && pass 'and was delivered' \
    || fail 'replay outcome' "state is $final"

  # The whole point: three origins, one pipeline, one audit trail.
  sources=$("${PSQL[@]}" "SELECT count(DISTINCT dispatch_source) FROM notification_attempts na
    JOIN notification_events ne ON ne.id=na.notification_event_id
    WHERE ne.event_id='EVT-E2E'")
  [[ "$sources" == "3" ]] \
    && pass 'the audit trail records all three origins for one event' \
    || fail 'audit trail origins' "got $sources distinct sources, want 3"
else
  fail 'skipping the replay checks' 'the event never reached FAILED'
fi

deliveredEvent=$("${PSQL[@]}" "SELECT id FROM notification_events WHERE state='DELIVERED' AND client_id='CLIENT001' AND event_id <> 'EVT-E2E' LIMIT 1")
expect_status 409 'a delivered event cannot be replayed' \
  -X POST "$SELF/notification_events/$deliveredEvent/replay" -H "authorization: Bearer $TOKEN_A"

# ---------------------------------------------------------------------------
step 'Observability'
observed=$(curl -s "$MONITOR/api/summary" | json 'd["total"]')
[[ "${observed:-0}" -gt 0 ]] && pass "the monitor observed $observed deliveries" \
  || fail 'the monitor saw nothing'

curl -s http://localhost:9101/metrics | grep -q notifications_delivery_attempts_total \
  && pass 'dispatch exports delivery metrics' || fail 'dispatch metrics missing'
curl -s http://localhost:9102/metrics | grep -q notifications_oldest_retrying_age_seconds \
  && pass 'retry exports the backlog gauge' || fail 'backlog gauge missing'

# ---------------------------------------------------------------------------
printf '\n\033[1m%d passed, %d failed\033[0m\n' "$passed" "$failed"
[[ "$failed" -eq 0 ]] || exit 1

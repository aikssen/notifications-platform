#!/bin/bash
set -euo pipefail

BOOTSTRAP="${KAFKA_BOOTSTRAP:-kafka:29092}"

create() {
  local topic="$1" partitions="$2"
  kafka-topics --bootstrap-server "$BOOTSTRAP" \
    --create --if-not-exists \
    --topic "$topic" \
    --partitions "$partitions" \
    --replication-factor 1
  echo "topic ready: $topic (partitions=$partitions)"
}

# Delivery pipeline. Multiple partitions so dispatch-service scales
# horizontally; producers key by client_id, which preserves per-client
# ordering while still allowing parallel consumption.
create "${TOPIC_DISPATCH:-notifications.dispatch}" "${DISPATCH_PARTITIONS:-3}"

# Delivery outcomes. Observability consumes this stream — it is not coupled
# to the delivery path in any way.
create "${TOPIC_RESULT:-notifications.delivery-result}" 1

# Dead letter queue: events whose retry budget is exhausted.
create "${TOPIC_DLQ:-notifications.dlq}" 1

echo "--- topics ---"
kafka-topics --bootstrap-server "$BOOTSTRAP" --list

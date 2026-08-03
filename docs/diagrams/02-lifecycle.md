# The delivery lifecycle

One state machine, defended in one place:
`services/dispatch-service/internal/domain/notification_event.go`.

```mermaid
stateDiagram-v2
  [*] --> PENDING : ingested from Kafka<br/>(idempotent on event_id)

  PENDING --> DELIVERING : claimed<br/>compare-and-set

  DELIVERING --> DELIVERED : webhook returned<br/>the declared status
  DELIVERING --> RETRYING : delivery failed
  DELIVERING --> FAILED : no active subscription

  RETRYING --> DELIVERING : retry-service requeued it

  FAILED --> RETRYING : client replay<br/>(SELF_SERVICE)

  DELIVERED --> [*]

  note right of DELIVERING
    A dispatcher that dies here would strand
    the event forever — nothing else looks at
    DELIVERING. The retry service sweeps rows
    older than RETRY_STALLED_AFTER_SECONDS
    back to RETRYING.
  end note

  note right of FAILED
    Reachable on purpose. The case statement
    asks to re-send "when delivery has
    definitely failed", so a state has to mean
    exactly that. dispatch never writes it —
    the retry service does, when the budget
    runs out.
  end note
```

## Why the transitions are a table, not a set of `if`s

```go
var allowedTransitions = map[EventState][]EventState{
	StatePending:    {StateDelivering},
	StateDelivering: {StateDelivered, StateRetrying, StateFailed},
	StateRetrying:   {StateDelivering},
	StateDelivered:  {},              // terminal
	StateFailed:     {StateRetrying}, // a replay re-opens it
}
```

Every legal and illegal edge is asserted in
`notification_event_test.go`, so widening the machine is a deliberate act that
shows up in a diff, not an emergent property of scattered conditionals.

## The two defects this shape fixes

The previous implementation had `FAILED` unreachable: a failed delivery went
back to `PENDING`, so nothing ever declared an event definitively failed. The
replay endpoint then guarded on the wrong states — it rejected `FAILED` and
accepted `PENDING`. Both bugs were invisible because they were consistent with
each other.

Making the transition table explicit is what makes that class of error a
compile-and-test failure instead of a silent behaviour.

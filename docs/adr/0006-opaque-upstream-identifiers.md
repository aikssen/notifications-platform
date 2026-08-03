# 6. Upstream identifiers are opaque strings

**Status:** accepted

## Context

The original schema declared `event_id` and `client_id` as `UUID`. The fixture
attached to the case statement uses `EVT001` and `CLIENT001`, which do not fit.

## Decision

`id` — the identifier we generate — stays `UUID`. `event_id` and `client_id`
are `VARCHAR(100)`, indexed, with `event_id` unique.

## Consequences

We do not control the format of another system's identifiers. Declaring them as
UUID asserts something about the upstream platform that we cannot enforce and
that the very first sample of real data contradicted.

The practical effect is that the case statement's fixture loads verbatim. There
is no mapping table, no UUIDv5 derivation to explain, and the identifiers a
reviewer sees in the API are the ones they see in the file.

The cost is four bytes per row and losing UUID type validation on two columns —
validation that was checking the wrong thing anyway.

## Note

The file-based repository adapter *does* derive a stable UUIDv5 for its
`notification_event_id`, because that identifier is ours and the file does not
carry one. The namespace is fixed, so the same fixture event resolves to the
same id from the file adapter, from the seeder and across restarts.

-- =====================================================================
-- Notifications platform — schema
--
-- Identifier strategy:
--   `id`        is OUR identifier: a UUID we generate and control.
--   `event_id`  and `client_id` are identifiers produced by UPSTREAM systems.
--               We do not get to choose their format, so we store them as
--               opaque, indexed strings. This is why the challenge fixture
--               (EVT001 / CLIENT001) loads verbatim, with no mapping trick.
-- =====================================================================


-- ---------------------------------------------------------------------
-- subscriptions — which event of which client goes where
-- ---------------------------------------------------------------------
CREATE TABLE subscriptions (
    id              UUID         PRIMARY KEY,
    client_id       VARCHAR(100) NOT NULL,
    event_type      VARCHAR(100) NOT NULL,
    webhook_url     TEXT         NOT NULL,
    http_method     VARCHAR(10)  NOT NULL DEFAULT 'POST',
    expected_status INTEGER      NOT NULL DEFAULT 200,

    -- per-subscription secret used to sign outgoing webhooks (X-Signature).
    -- Returned to the client exactly once, at creation time.
    hmac_secret     VARCHAR(128) NOT NULL,

    status          VARCHAR(20)  NOT NULL DEFAULT 'ACTIVE',
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),

    -- one active route per (client, event type): makes resolution deterministic
    CONSTRAINT uq_subscriptions_client_event
        UNIQUE (client_id, event_type),

    CONSTRAINT chk_subscriptions_status
        CHECK (status IN ('ACTIVE', 'INACTIVE')),

    CONSTRAINT chk_subscriptions_method
        CHECK (http_method IN ('POST', 'PUT', 'PATCH')),

    CONSTRAINT chk_subscriptions_expected_status
        CHECK (expected_status BETWEEN 100 AND 599)
);

-- resolve() looks up exactly this triple, so index it as one
CREATE INDEX idx_subscriptions_resolve
    ON subscriptions (client_id, event_type)
    WHERE status = 'ACTIVE';

CREATE INDEX idx_subscriptions_client_id
    ON subscriptions (client_id);


-- ---------------------------------------------------------------------
-- notification_events — the business event and its global delivery state
-- ---------------------------------------------------------------------
CREATE TABLE notification_events (
    id            UUID         PRIMARY KEY,   -- notification_event_id (ours)
    event_id      VARCHAR(100) NOT NULL,      -- upstream id == idempotency key
    client_id     VARCHAR(100) NOT NULL,
    event_type    VARCHAR(100) NOT NULL,
    event_payload JSONB        NOT NULL,
    state         VARCHAR(20)  NOT NULL,

    -- asynchronous retry bookkeeping, owned by retry-service
    retry_count   INTEGER      NOT NULL DEFAULT 0,
    next_retry_at TIMESTAMPTZ,
    last_error    TEXT,

    created_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),

    -- idempotency: the same upstream event can never be ingested twice
    CONSTRAINT uq_notification_events_event_id
        UNIQUE (event_id),

    CONSTRAINT chk_notification_events_state
        CHECK (state IN (
            'PENDING',      -- ingested, not yet attempted
            'DELIVERING',   -- an attempt is in flight
            'RETRYING',     -- attempt failed, eligible for another one
            'DELIVERED',    -- terminal: webhook accepted it
            'FAILED'        -- terminal: retries exhausted -> replayable
        ))
);

-- self-service list: always scoped by client, ordered by recency
CREATE INDEX idx_notification_events_client_created
    ON notification_events (client_id, created_at DESC);

-- self-service filter by delivery_status within a client
CREATE INDEX idx_notification_events_client_state
    ON notification_events (client_id, state);

-- retry-service poller: only ever scans RETRYING rows that are due
CREATE INDEX idx_notification_events_due_retry
    ON notification_events (next_retry_at)
    WHERE state = 'RETRYING';

CREATE INDEX idx_notification_events_state
    ON notification_events (state);


-- ---------------------------------------------------------------------
-- notification_attempts — the audit trail: one row per delivery cycle
--
-- Immediate in-process HTTP retries are NOT persisted here. They are
-- transport-level noise, visible in logs and metrics. One row == one
-- meaningful delivery cycle, with its consolidated outcome.
-- ---------------------------------------------------------------------
CREATE TABLE notification_attempts (
    id                    UUID         PRIMARY KEY,
    notification_event_id UUID         NOT NULL,
    attempt_number        INTEGER      NOT NULL,

    -- who triggered this cycle: keeps a single delivery pipeline traceable
    dispatch_source       VARCHAR(20)  NOT NULL,
    status                VARCHAR(10)  NOT NULL,

    -- request as actually sent
    webhook_url           TEXT         NOT NULL,
    request_method        VARCHAR(10)  NOT NULL,
    request_payload       JSONB        NOT NULL,

    -- response as actually received. Non-JSON bodies are stored as
    -- {"raw": "<body>"} so the column stays queryable.
    response_status       INTEGER,
    response_body         JSONB,
    error_message         TEXT,
    duration_ms           INTEGER,

    attempted_at          TIMESTAMPTZ  NOT NULL DEFAULT now(),

    CONSTRAINT fk_notification_attempts_event
        FOREIGN KEY (notification_event_id)
        REFERENCES notification_events (id)
        ON DELETE CASCADE,

    CONSTRAINT chk_notification_attempts_status
        CHECK (status IN ('SUCCESS', 'FAILED')),

    CONSTRAINT chk_notification_attempts_source
        CHECK (dispatch_source IN (
            'SYSTEM',         -- first delivery, driven by the platform event
            'RETRY_SERVICE',  -- automatic asynchronous retry
            'SELF_SERVICE'    -- manual replay requested by the client
        )),

    CONSTRAINT chk_notification_attempts_number
        CHECK (attempt_number > 0)
);

CREATE INDEX idx_notification_attempts_event
    ON notification_attempts (notification_event_id, attempt_number);

CREATE INDEX idx_notification_attempts_attempted_at
    ON notification_attempts (attempted_at DESC);

-- Guards the attempt sequence under concurrency. dispatch-service allocates
-- attempt_number in a single INSERT..SELECT and retries on violation of this
-- index, which is what makes running multiple dispatch instances safe.
CREATE UNIQUE INDEX uq_notification_attempts_number_per_event
    ON notification_attempts (notification_event_id, attempt_number);

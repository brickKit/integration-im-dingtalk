-- The DingTalk channel member (docs/design.md).

-- ID10: a principal's DingTalk user, bound by the principal through DingTalk's OAuth; an administrator only ends it
CREATE TABLE binding (
    id         uuid PRIMARY KEY,
    principal  text NOT NULL,
    user_id    text NOT NULL,
    state      text NOT NULL CHECK (state IN ('active', 'ended')),
    bound_at   timestamptz NOT NULL DEFAULT now(),
    ended_at   timestamptz,
    ended_by   text
);
CREATE UNIQUE INDEX binding_principal ON binding (principal) WHERE state = 'active';
CREATE UNIQUE INDEX binding_user ON binding (user_id) WHERE state = 'active';
CREATE INDEX binding_ended ON binding (ended_at) WHERE state = 'ended';

-- ID10: a binding in progress: the state's digest only, ten minutes, used once
CREATE TABLE bind_session (
    digest     text PRIMARY KEY,
    principal  text NOT NULL,
    expires_at timestamptz NOT NULL,
    used_at    timestamptz
);

-- ID01-ID04: a delivery handed over by the notification component; each action's one-time value as a digest only
CREATE TABLE delivery (
    caller       text NOT NULL,
    id           uuid NOT NULL,
    digest       text NOT NULL,
    sender       text NOT NULL,
    intent_id    uuid NOT NULL,
    collapse_key text,
    collapse_seq bigint,
    principal    text NOT NULL,
    user_id      text NOT NULL,
    category     text NOT NULL,
    title        text NOT NULL,
    link         text NOT NULL,
    expires_at   timestamptz NOT NULL,
    grant_id     uuid,
    actions      jsonb NOT NULL DEFAULT '[]', -- [{"action", "label", "nonce_digest"}]
    command_id   uuid NOT NULL,
    result       text CHECK (result IN ('sent', 'failed', 'rejected', 'expired')),
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (caller, id)
);
CREATE INDEX delivery_created ON delivery (created_at);

-- ID06-ID09: a callback as received (the one-time value replaced by its digest), then processed
CREATE TABLE callback (
    event_id     text PRIMARY KEY,
    received_at  timestamptz NOT NULL DEFAULT now(),
    cipher_sha   text NOT NULL,
    signature    text NOT NULL,
    timestamp    text NOT NULL,
    nonce        text NOT NULL,
    body         jsonb NOT NULL,
    state        text NOT NULL CHECK (state IN ('received', 'published', 'quarantined', 'ignored')),
    reason       text,
    processed_at timestamptz
);
CREATE INDEX callback_state ON callback (state, received_at DESC, event_id DESC);

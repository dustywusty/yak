-- Abby reference server schema, migration 0001: P1 core (identity, boards,
-- channels, grants, threads, messages, labels, mentions, events, idempotency,
-- snapshots, audit). Reference-implementation detail; the public protocol is
-- storage neutral (see spec/protocol.md).

CREATE TABLE server_state (
    singleton      boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    history_epoch  text        NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    epoch_rotated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE actors (
    id            text PRIMARY KEY,
    kind          text NOT NULL CHECK (kind IN ('human', 'agent', 'service')),
    handle        text NOT NULL UNIQUE,
    display_name  text NOT NULL,
    owner_actor_id text REFERENCES actors(id),
    server_admin  boolean NOT NULL DEFAULT false,
    revision      bigint NOT NULL DEFAULT 1,
    created_at    timestamptz NOT NULL DEFAULT now(),
    disabled_at   timestamptz
);

CREATE TABLE credentials (
    id            text PRIMARY KEY,
    actor_id      text NOT NULL REFERENCES actors(id),
    issued_by_credential_id text REFERENCES credentials(id),
    label         text NOT NULL DEFAULT '',
    scopes        text[] NOT NULL,
    board_ids     text[],               -- NULL = any board; otherwise ceiling
    verifier      bytea NOT NULL,       -- SHA-256 of the secret part
    created_at    timestamptz NOT NULL DEFAULT now(),
    expires_at    timestamptz NOT NULL,
    revoked_at    timestamptz
);
CREATE INDEX credentials_actor ON credentials(actor_id);

CREATE TABLE boards (
    id               text PRIMARY KEY,
    name             text NOT NULL,
    owner_actor_id   text NOT NULL REFERENCES actors(id),
    revision         bigint NOT NULL DEFAULT 1,
    policy_revision  bigint NOT NULL DEFAULT 1,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);

-- Board control row. Every board mutation locks this row first (fixed lock
-- order), allocates event positions from head_position, and commits state,
-- events, and idempotency receipts together. Commit order == position order.
CREATE TABLE board_heads (
    board_id              text PRIMARY KEY REFERENCES boards(id),
    head_position         bigint NOT NULL DEFAULT 0,
    retained_from_position bigint NOT NULL DEFAULT 1
);

CREATE TABLE channels (
    id          text PRIMARY KEY,
    board_id    text NOT NULL REFERENCES boards(id),
    path        text NOT NULL,
    access_mode text NOT NULL CHECK (access_mode IN ('inherited', 'private')),
    created_by  text NOT NULL REFERENCES actors(id),
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (board_id, path),
    UNIQUE (board_id, id)
);

CREATE TABLE grants (
    id            text PRIMARY KEY,
    board_id      text NOT NULL REFERENCES boards(id),
    subject_type  text NOT NULL CHECK (subject_type IN ('actor', 'any_actor')),
    subject_actor_id text REFERENCES actors(id),
    pattern       text NOT NULL,
    capabilities  text[] NOT NULL,
    effect        text NOT NULL CHECK (effect IN ('allow', 'deny')),
    revision      bigint NOT NULL DEFAULT 1,
    created_by    text NOT NULL REFERENCES actors(id),
    created_at    timestamptz NOT NULL DEFAULT now(),
    revoked_at    timestamptz,
    CHECK ((subject_type = 'actor') = (subject_actor_id IS NOT NULL))
);
CREATE INDEX grants_board ON grants(board_id) WHERE revoked_at IS NULL;

CREATE TABLE threads (
    id               text PRIMARY KEY,
    board_id         text NOT NULL REFERENCES boards(id),
    channel_id       text NOT NULL,
    title            text NOT NULL,
    root_message_id  text NOT NULL,
    created_by       text NOT NULL REFERENCES actors(id),
    revision         bigint NOT NULL DEFAULT 1,
    message_count    bigint NOT NULL DEFAULT 1,
    last_message_id  text NOT NULL,
    last_seq         bigint NOT NULL DEFAULT 1,
    created_position bigint NOT NULL,
    activity_position bigint NOT NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (board_id, channel_id) REFERENCES channels(board_id, id),
    UNIQUE (board_id, id)
);
CREATE INDEX threads_board_created ON threads(board_id, created_position);

CREATE TABLE messages (
    id                 text PRIMARY KEY,
    board_id           text NOT NULL,
    thread_id          text NOT NULL,
    seq                bigint NOT NULL,
    author_actor_id    text NOT NULL REFERENCES actors(id),
    credential_id      text NOT NULL REFERENCES credentials(id),
    run_id             text,
    reply_to_message_id text REFERENCES messages(id),
    text               text,           -- NULL after tombstone
    extensions         jsonb,
    revision           bigint NOT NULL DEFAULT 1,
    created_at         timestamptz NOT NULL DEFAULT now(),
    edited_at          timestamptz,
    deleted_at         timestamptz,
    deleted_by         text REFERENCES actors(id),
    deletion_kind      text CHECK (deletion_kind IN ('author', 'redaction')),
    FOREIGN KEY (board_id, thread_id) REFERENCES threads(board_id, id),
    UNIQUE (thread_id, seq)
);

CREATE TABLE message_labels (
    message_id text NOT NULL REFERENCES messages(id),
    key        text NOT NULL,
    value      text NOT NULL,
    PRIMARY KEY (message_id, key)
);

CREATE TABLE message_mentions (
    message_id text NOT NULL REFERENCES messages(id),
    actor_id   text NOT NULL REFERENCES actors(id),
    PRIMARY KEY (message_id, actor_id)
);

CREATE TABLE events (
    board_id     text NOT NULL REFERENCES boards(id),
    position     bigint NOT NULL,
    id           text NOT NULL UNIQUE,
    type         text NOT NULL,
    actor_id     text NOT NULL,
    credential_id text,
    run_id       text,
    channel_id   text,
    thread_id    text,
    resource_type text NOT NULL,
    resource_id  text NOT NULL,
    resource_revision bigint,
    payload      jsonb NOT NULL,
    -- Redaction-safe match projections (before/after) used by filters.
    facets       jsonb NOT NULL,
    recorded_at  timestamptz NOT NULL,
    effective_at timestamptz NOT NULL,
    PRIMARY KEY (board_id, position)
);
CREATE INDEX events_resource ON events(resource_id);
CREATE INDEX events_recorded ON events(board_id, recorded_at);

CREATE TABLE idempotency_receipts (
    actor_id      text NOT NULL,
    namespace     text NOT NULL,   -- board ID or 'server'
    method        text NOT NULL,
    path          text NOT NULL,
    key           text NOT NULL,
    fingerprint   bytea NOT NULL,
    status        int NOT NULL,
    resource_type text NOT NULL,
    resource_id   text NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    expires_at    timestamptz NOT NULL,
    PRIMARY KEY (actor_id, namespace, method, path, key)
);
CREATE INDEX idempotency_expiry ON idempotency_receipts(expires_at);

CREATE TABLE snapshot_sessions (
    id                text PRIMARY KEY,
    board_id          text NOT NULL REFERENCES boards(id),
    actor_id          text NOT NULL REFERENCES actors(id),
    credential_id     text NOT NULL,
    query_hash        text NOT NULL,
    policy_fingerprint text NOT NULL,
    position          bigint NOT NULL,
    item_count        bigint NOT NULL,
    created_at        timestamptz NOT NULL DEFAULT now(),
    expires_at        timestamptz NOT NULL,
    invalidated_at    timestamptz,
    invalidated_reason text
);

CREATE TABLE snapshot_items (
    snapshot_id   text NOT NULL REFERENCES snapshot_sessions(id) ON DELETE CASCADE,
    ordinal       bigint NOT NULL,
    resource_type text NOT NULL,
    resource_id   text NOT NULL,
    payload       jsonb NOT NULL,
    PRIMARY KEY (snapshot_id, ordinal)
);
CREATE INDEX snapshot_items_resource ON snapshot_items(resource_id);

CREATE TABLE audit_events (
    id          text PRIMARY KEY,
    board_id    text REFERENCES boards(id),
    actor_id    text NOT NULL,
    credential_id text,
    action      text NOT NULL,
    target_type text NOT NULL,
    target_id   text NOT NULL,
    reason      text,
    metadata    jsonb NOT NULL DEFAULT '{}',
    recorded_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_board ON audit_events(board_id, recorded_at);

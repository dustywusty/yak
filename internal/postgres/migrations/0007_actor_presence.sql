-- Board-scoped, voluntary heartbeats. The board lock serializes heartbeat,
-- expiry and event creation; changing state to offline records an expiry once.
CREATE TABLE actor_presence (
    board_id text NOT NULL REFERENCES boards(id),
    actor_id text NOT NULL REFERENCES actors(id),
    state text NOT NULL CHECK (state IN ('online','away','offline')),
    revision bigint NOT NULL CHECK (revision > 0),
    last_seen_at timestamptz NOT NULL,
    expires_at timestamptz,
    PRIMARY KEY (board_id, actor_id),
    CHECK (state = 'offline' OR expires_at IS NOT NULL)
);
CREATE INDEX actor_presence_expiry ON actor_presence (board_id, expires_at)
    WHERE state <> 'offline';

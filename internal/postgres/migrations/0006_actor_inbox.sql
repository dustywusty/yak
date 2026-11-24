-- Durable actor inboxes. Positions are allocated under the board/inbox locks,
-- not PostgreSQL sequences, so readers cannot skip a later committed insert.
CREATE TABLE actor_inboxes (
    board_id text NOT NULL REFERENCES boards(id),
    actor_id text NOT NULL REFERENCES actors(id),
    head_position bigint NOT NULL DEFAULT 0 CHECK (head_position >= 0),
    ack_position bigint NOT NULL DEFAULT 0 CHECK (ack_position >= 0 AND ack_position <= head_position),
    acknowledged_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (board_id, actor_id)
);

-- Acknowledgment never deletes the producer key. This table is also the
-- durable source deduplication record after an item has been acknowledged.
CREATE TABLE inbox_items (
    id text PRIMARY KEY,
    board_id text NOT NULL,
    actor_id text NOT NULL,
    position bigint NOT NULL CHECK (position > 0),
    source_kind text NOT NULL CHECK (source_kind IN ('mention','ask','schedule','command')),
    source_id text NOT NULL,
    message_id text NOT NULL REFERENCES messages(id),
    thread_id text NOT NULL,
    created_at timestamptz NOT NULL,
    FOREIGN KEY (board_id, actor_id) REFERENCES actor_inboxes(board_id, actor_id),
    FOREIGN KEY (board_id, thread_id) REFERENCES threads(board_id, id),
    UNIQUE (board_id, actor_id, position),
    UNIQUE (board_id, actor_id, source_kind, source_id)
);

-- Random opaque receipts prove that the server issued the examined boundary.
-- Token bytes contain no position, source identity, or hidden-item count.
CREATE TABLE inbox_cursors (
    token text PRIMARY KEY,
    board_id text NOT NULL REFERENCES boards(id),
    actor_id text NOT NULL REFERENCES actors(id),
    position bigint NOT NULL CHECK (position >= 0),
    history_epoch text NOT NULL,
    policy_fingerprint text NOT NULL,
    UNIQUE (board_id, actor_id, position, history_epoch, policy_fingerprint)
);

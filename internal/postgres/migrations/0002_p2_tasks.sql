-- Abby reference server schema, migration 0002: P2 tasks and claims
-- (spec/p2-tasks.md, ADR-0004, ADR-0008, ADR-0016 §5). Reference detail; the
-- protocol is storage neutral.

-- Effective task state at time `at` (ADR-0004): a recorded claim whose lease
-- is over reads as `ready` until the lapse is materialized.
CREATE FUNCTION abby_task_effective_state(state text, claim_expires_at timestamptz, at timestamptz)
RETURNS text LANGUAGE sql IMMUTABLE AS $$
    SELECT CASE WHEN state = 'claimed' AND claim_expires_at <= at THEN 'ready' ELSE state END
$$;

CREATE TABLE task_batches (
    id          text PRIMARY KEY,
    board_id    text NOT NULL REFERENCES boards(id),
    created_by  text NOT NULL REFERENCES actors(id),
    items       jsonb NOT NULL,                       -- [{ref, task_id}] in request order
    created_at  timestamptz NOT NULL
);

CREATE TABLE tasks (
    id                 text PRIMARY KEY,
    board_id           text NOT NULL REFERENCES boards(id),
    thread_id          text NOT NULL,
    channel_id         text NOT NULL,                 -- copy of the thread channel (threads do not move)
    owner_actor_id     text NOT NULL REFERENCES actors(id),
    assignee_actor_id  text REFERENCES actors(id),
    created_by         text NOT NULL REFERENCES actors(id),
    origin             text NOT NULL CHECK (origin IN ('new_thread','converted','batch')),
    batch_id           text REFERENCES task_batches(id),
    state              text NOT NULL CHECK (state IN
                         ('waiting','ready','claimed','blocked','completed','failed','canceled')),
    revision           bigint NOT NULL DEFAULT 1,
    epoch              bigint NOT NULL DEFAULT 0,
    retry_count        int    NOT NULL DEFAULT 0,
    brief_revision     int    NOT NULL DEFAULT 1,
    current_claim_id   text,                          -- set iff state = 'claimed'
    claim_expires_at   timestamptz,                   -- copy of the claim's expires_at
    created_position   bigint NOT NULL,               -- position of task.created
    block              jsonb,                         -- {reason, cause, by_actor_id, at}
    outcome            jsonb,                         -- spec §19.1
    root_redacted_at   timestamptz,
    created_at         timestamptz NOT NULL,
    updated_at         timestamptz NOT NULL,
    state_changed_at   timestamptz NOT NULL,
    UNIQUE (board_id, id),
    UNIQUE (thread_id),                               -- at most one task per thread
    FOREIGN KEY (board_id, thread_id)  REFERENCES threads(board_id, id),
    FOREIGN KEY (board_id, channel_id) REFERENCES channels(board_id, id),
    CHECK ((state = 'claimed') = (current_claim_id IS NOT NULL)),
    CHECK ((state = 'claimed') = (claim_expires_at IS NOT NULL))
);
CREATE INDEX tasks_ready_queue    ON tasks (board_id, created_position) WHERE state = 'ready';
CREATE INDEX tasks_ready_assignee ON tasks (board_id, assignee_actor_id, created_position) WHERE state = 'ready';
CREATE INDEX tasks_claim_expiry   ON tasks (board_id, claim_expires_at) WHERE state = 'claimed';
CREATE INDEX tasks_board_created  ON tasks (board_id, created_position);

CREATE TABLE claims (
    id                text PRIMARY KEY,
    board_id          text NOT NULL,
    task_id           text NOT NULL,
    actor_id          text NOT NULL REFERENCES actors(id),
    credential_id     text NOT NULL REFERENCES credentials(id),
    run_id            text,
    execution_id      text NOT NULL UNIQUE,
    epoch             bigint NOT NULL,
    brief_revision    int    NOT NULL,
    lease_seconds     int    NOT NULL,
    status            text NOT NULL CHECK (status IN
                        ('active','released','expired','completed','failed','revoked')),
    revision          bigint NOT NULL DEFAULT 1,      -- +1 per renewal
    acquired_at       timestamptz NOT NULL,
    expires_at        timestamptz NOT NULL,
    ended_at          timestamptz,                    -- = expires_at for lapses
    end_recorded_at   timestamptz,
    proof_verifier    bytea NOT NULL,                 -- SHA-256(secret)
    proof_ciphertext  bytea,                          -- AES-256-GCM; NULL once ended or without a key
    proof_key_id      text,
    FOREIGN KEY (board_id, task_id) REFERENCES tasks(board_id, id),
    UNIQUE (task_id, epoch)
);
-- CLAIM-002 belt and braces (recorded-active; lapses stay 'active' until materialized).
CREATE UNIQUE INDEX claims_one_active ON claims (task_id) WHERE status = 'active';
CREATE INDEX claims_task_history ON claims (task_id, epoch DESC);

CREATE TABLE task_briefs (
    board_id          text NOT NULL,
    task_id           text NOT NULL,
    revision          int  NOT NULL,
    text              text,                           -- NULL once redacted
    source_kind       text NOT NULL CHECK (source_kind IN ('root','brief_update')),
    source_message_id text NOT NULL REFERENCES messages(id),
    source_message_revision bigint NOT NULL,
    reason            text,
    created_by        text NOT NULL REFERENCES actors(id),
    created_at        timestamptz NOT NULL,
    redacted_at       timestamptz,
    PRIMARY KEY (task_id, revision),
    FOREIGN KEY (board_id, task_id) REFERENCES tasks(board_id, id)
);

CREATE TABLE task_dependencies (
    board_id            text NOT NULL,
    task_id             text NOT NULL,                -- dependent
    depends_on_task_id  text NOT NULL,                -- prerequisite
    created_by          text NOT NULL REFERENCES actors(id),
    created_at          timestamptz NOT NULL,
    PRIMARY KEY (task_id, depends_on_task_id),
    FOREIGN KEY (board_id, task_id)            REFERENCES tasks(board_id, id),
    FOREIGN KEY (board_id, depends_on_task_id) REFERENCES tasks(board_id, id),
    CHECK (task_id <> depends_on_task_id)
);
CREATE INDEX task_dependencies_reverse ON task_dependencies (board_id, depends_on_task_id);

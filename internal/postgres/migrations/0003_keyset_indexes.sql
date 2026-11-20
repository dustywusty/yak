-- Abby reference server schema, migration 0003: indexes for the keyset
-- pagination of administrative lists (PAGE-001). Each list reads one page
-- per query in keyset order; these indexes let a page read only its rows
-- instead of sorting every candidate. Reference detail; the protocol is
-- storage neutral.

CREATE INDEX boards_keyset ON boards(created_at, id);
CREATE INDEX actors_keyset ON actors(created_at, id);
CREATE INDEX grants_keyset ON grants(board_id, created_at, id);

-- Replaces credentials_actor (actor_id), a prefix of this index.
CREATE INDEX credentials_keyset ON credentials(actor_id, created_at, id);
DROP INDEX credentials_actor;

-- Audit lists are newest first: (recorded_at, id) descending, read by a
-- backward index scan. Replaces audit_board (board_id, recorded_at), a prefix
-- of the board index.
CREATE INDEX audit_board_keyset ON audit_events(board_id, recorded_at, id);
DROP INDEX audit_board;
CREATE INDEX audit_keyset ON audit_events(recorded_at, id);

-- Human gate marker. Existing rows keep their behavior until a manager sets it.
ALTER TABLE tasks ADD COLUMN direct_action_required boolean NOT NULL DEFAULT false;

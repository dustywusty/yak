-- Existing credentials retain their authority. The defaults also cover
-- bootstrap and older administrative inserts; API children inherit their issuer.
ALTER TABLE credentials
    ADD COLUMN may_manage_credentials boolean NOT NULL DEFAULT true,
    ADD COLUMN may_delegate boolean NOT NULL DEFAULT true;

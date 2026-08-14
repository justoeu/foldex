-- 000024_oauth_link_step_up
--
-- Linking an OAuth identity changes the account's credential set. A session
-- cookie alone is not enough proof: link states bind a recent password/2FA
-- step-up to the exact live session and credential epoch that produced it.

ALTER TABLE oauth_state
    ADD COLUMN session_id    BIGINT REFERENCES session(id) ON DELETE CASCADE,
    ADD COLUMN token_version INTEGER,
    ADD COLUMN proof_at      TIMESTAMPTZ;

-- States created by the old proofless GET contract cannot be upgraded into a
-- valid step-up proof. They are short-lived and safe only when invalidated.
DELETE FROM oauth_state WHERE purpose = 'link';

ALTER TABLE oauth_state
    ADD CONSTRAINT oauth_state_link_proof_check CHECK (
        purpose <> 'link' OR (
            user_id IS NOT NULL AND
            session_id IS NOT NULL AND
            token_version IS NOT NULL AND
            proof_at IS NOT NULL
        )
    );

CREATE INDEX oauth_state_session_idx
    ON oauth_state (session_id)
    WHERE session_id IS NOT NULL;

-- 000021_credential_coherence
--
-- An ACTIVE account must always hold at least one way to sign in: a password,
-- or a linked provider identity. Nothing enforced that until now, and PR4 is
-- what makes it reachable — the Google conversion sets password_hash to NULL in
-- the same transaction that inserts the identity, and unlinking removes the
-- identity. A bug that ordered either of those wrongly would leave an account
-- that no UI can ever sign into and no UI can ever repair: the reset flow needs
-- a password credential to reset, and the OAuth flow needs an identity to match.
-- Recovery would be a manual UPDATE by whoever has psql.
--
-- This is a CONSTRAINT TRIGGER rather than a CHECK because the invariant spans
-- two tables, and DEFERRABLE INITIALLY DEFERRED because it is legitimately
-- violated *inside* a transaction: the conversion nulls the password and adds
-- the identity as two statements, and one order or the other is momentarily
-- credential-less. What must hold is the state at COMMIT.
--
-- 'pending' and 'disabled' are deliberately outside the rule. The bootstrap
-- placeholder ships as pending with no password at all — that row is what the
-- setup screen claims — and a disabled account is not supposed to sign in.

-- ─────────────────────────────────────────────────────────────────────
-- 1. The Google subject a conversion challenge is about
-- ─────────────────────────────────────────────────────────────────────
--
-- When the OAuth callback finds an unknown subject whose e-mail matches an
-- existing password account, it opens a 'convert_google' challenge and answers
-- with a redirect. The subject Google vouched for has to survive until the
-- convert POST arrives — and it must NOT travel through the browser, or the
-- client could name a different subject than the one that was authenticated and
-- attach someone else's Google account to the password it just proved.
--
-- Nullable, because every other challenge purpose leaves them empty.
ALTER TABLE auth_challenge
    ADD COLUMN oauth_provider TEXT,
    ADD COLUMN oauth_subject  TEXT,
    ADD COLUMN oauth_email    TEXT;

-- ─────────────────────────────────────────────────────────────────────
-- 2. An active account always has a way in
-- ─────────────────────────────────────────────────────────────────────

CREATE OR REPLACE FUNCTION assert_active_user_has_credential(uid BIGINT)
RETURNS void AS $$
DECLARE
    ok BOOLEAN;
BEGIN
    SELECT u.status <> 'active'
           OR u.password_hash IS NOT NULL
           OR EXISTS (SELECT 1 FROM user_identity i WHERE i.user_id = u.id)
      INTO ok
      FROM app_user u
     WHERE u.id = uid;

    -- No row means the account was deleted in this transaction; a deleted
    -- account cannot be credential-less.
    IF ok IS NOT NULL AND NOT ok THEN
        RAISE EXCEPTION
            'active account % would be left with no way to sign in', uid
            USING ERRCODE = 'check_violation';
    END IF;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION app_user_credential_guard()
RETURNS TRIGGER AS $$
BEGIN
    PERFORM assert_active_user_has_credential(NEW.id);
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION user_identity_credential_guard()
RETURNS TRIGGER AS $$
BEGIN
    PERFORM assert_active_user_has_credential(OLD.user_id);
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE CONSTRAINT TRIGGER app_user_credential_check
    AFTER INSERT OR UPDATE OF status, password_hash ON app_user
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION app_user_credential_guard();

-- Only DELETE. An INSERT into user_identity can only ever ADD a credential, and
-- firing there would make every link operation pay a lookup for a check that
-- cannot fail.
CREATE CONSTRAINT TRIGGER user_identity_credential_check
    AFTER DELETE ON user_identity
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION user_identity_credential_guard();

-- The conversion and the link flow both resolve an account by (provider,
-- subject); the unique index already covers that. What has no index is the
-- reverse lookup the account screen does — "which providers has THIS user
-- linked" — but user_identity_user_provider_uniq (user_id, provider) leads with
-- user_id, so it serves that too. No new index is needed here; this comment
-- exists so the next reader does not add a redundant one.

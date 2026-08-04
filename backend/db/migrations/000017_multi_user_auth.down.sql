-- 000017_multi_user_auth.down.sql
--
-- REVERSIBILITY IS PARTIAL AND THIS FILE IS DELIBERATELY FAIL-LOUD.
--
-- Schema is fully reversible. DATA is not:
--   * every app_user / user_identity / session / totp_secret / recovery_code /
--     api_token / invite / password_reset / auth_challenge / email_otp /
--     oauth_state row is DROPPED. There is no single-user representation.
--   * content rows SURVIVE, but the tenant boundary disappears: afterwards
--     every link/note/folder/tag belongs to "the user" again.
--   * the per-user master password is folded back into app_setting from the
--     FIRST admin only; other admins' master passwords are lost.
--
-- Restoring the global UNIQUE(url)/UNIQUE(name) can FAIL on a genuinely
-- multi-user database, because two tenants may hold the same value. That case
-- is guarded by an explicit RAISE so an operator gets a readable error instead
-- of a raw 23505, with the manual remedy printed. We refuse to silently delete
-- one tenant's rows to make a rollback fit.

DO $$
DECLARE dup_urls  bigint;
        dup_names bigint;
BEGIN
    SELECT count(*) INTO dup_urls  FROM (SELECT url  FROM link GROUP BY url  HAVING count(*) > 1) d;
    SELECT count(*) INTO dup_names FROM (SELECT name FROM tag  GROUP BY name HAVING count(*) > 1) d;
    IF dup_urls > 0 OR dup_names > 0 THEN
        RAISE EXCEPTION
            'cannot revert 000017: % duplicate link.url and % duplicate tag.name across users. '
            'Global UNIQUE(url)/UNIQUE(name) cannot be restored. Merge or delete the losing '
            'tenants'' rows first, e.g. keep the oldest owner: '
            'DELETE FROM link a USING link b WHERE a.url=b.url AND a.id>b.id;',
            dup_urls, dup_names;
    END IF;
END $$;

-- Fold the first admin's master password back into the global KV.
INSERT INTO app_setting (key, value, updated_at)
SELECT 'master_password_hash', u.master_password_hash, now()
FROM app_user u
WHERE u.master_password_hash IS NOT NULL
ORDER BY u.id LIMIT 1
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now();

INSERT INTO app_setting (key, value, updated_at)
SELECT 'master_password_hint', u.master_password_hint, now()
FROM app_user u
WHERE u.master_password_hint IS NOT NULL
ORDER BY u.id LIMIT 1
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now();

-- ─── Indexes back to their pre-000017 shapes ──────────────────────────
DROP INDEX IF EXISTS note_user_title_lower_idx;
DROP INDEX IF EXISTS note_user_body_trgm;
DROP INDEX IF EXISTS note_user_title_trgm;
CREATE INDEX note_title_trgm ON note USING gin (title     gin_trgm_ops);
CREATE INDEX note_body_trgm  ON note USING gin (body_text gin_trgm_ops);
DROP INDEX IF EXISTS note_user_pinned_created_idx;
CREATE INDEX note_pinned_created_idx ON note (pinned DESC, created_at DESC);
DROP INDEX IF EXISTS note_user_created_idx;
CREATE INDEX note_created_idx ON note (created_at DESC);

DROP INDEX IF EXISTS link_user_url_trgm;
DROP INDEX IF EXISTS link_user_title_trgm;
CREATE INDEX link_title_trgm ON link USING gin (title gin_trgm_ops);
CREATE INDEX link_url_trgm   ON link USING gin (url   gin_trgm_ops);
DROP INDEX IF EXISTS link_user_change_recent_idx;
CREATE INDEX link_change_recent_idx ON link (last_change_detected_at DESC)
    WHERE last_change_detected_at IS NOT NULL;
DROP INDEX IF EXISTS link_user_title_lower_idx;
CREATE INDEX link_title_lower_idx ON link (lower(title));
DROP INDEX IF EXISTS link_user_pinned_created_idx;
CREATE INDEX link_pinned_created ON link (pinned DESC, created_at DESC);
DROP INDEX IF EXISTS link_user_created_idx;
CREATE INDEX link_created ON link (created_at DESC);

DROP INDEX IF EXISTS push_subscription_user_idx;
DROP INDEX IF EXISTS folder_user_root_name_idx;
DROP INDEX IF EXISTS folder_user_parent_name_idx;

-- ─── Composite FKs back to plain ones ─────────────────────────────────
ALTER TABLE note   DROP CONSTRAINT note_folder_same_user_fkey;
ALTER TABLE note   ADD  CONSTRAINT note_folder_id_fkey
    FOREIGN KEY (folder_id) REFERENCES folder(id) ON DELETE SET NULL;
ALTER TABLE link   DROP CONSTRAINT link_folder_same_user_fkey;
ALTER TABLE link   ADD  CONSTRAINT link_folder_id_fkey
    FOREIGN KEY (folder_id) REFERENCES folder(id) ON DELETE SET NULL;
ALTER TABLE folder DROP CONSTRAINT folder_parent_same_user_fkey;
ALTER TABLE folder ADD  CONSTRAINT folder_parent_id_fkey
    FOREIGN KEY (parent_id) REFERENCES folder(id) ON DELETE SET NULL;
ALTER TABLE folder DROP CONSTRAINT folder_user_id_unique;

-- ─── Unique swaps back ────────────────────────────────────────────────
ALTER TABLE tag  DROP CONSTRAINT tag_user_name_unique;
ALTER TABLE tag  ADD  CONSTRAINT tag_name_key UNIQUE (name);
ALTER TABLE link DROP CONSTRAINT link_user_url_unique;
ALTER TABLE link ADD  CONSTRAINT link_url_unique UNIQUE (url);

-- ─── Ownership columns (their FKs go with them) ───────────────────────
ALTER TABLE push_subscription DROP COLUMN user_id;
ALTER TABLE note              DROP COLUMN user_id;
ALTER TABLE link              DROP COLUMN user_id;
ALTER TABLE folder            DROP COLUMN user_id;
ALTER TABLE tag               DROP COLUMN user_id;

DROP TABLE oauth_state;
DROP TABLE api_token;
DROP TABLE recovery_code;
DROP TABLE totp_secret;
DROP TABLE email_otp;
DROP TABLE auth_challenge;
DROP TABLE password_reset;
DROP TABLE invite;
DROP TABLE session_used_token;
DROP TABLE session;
DROP TABLE user_identity;
DROP TABLE app_user;

-- btree_gin is left installed: dropping a shared extension could break
-- unrelated objects and it is inert when unused.

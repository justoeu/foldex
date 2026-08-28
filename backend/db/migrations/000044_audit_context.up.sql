-- Audit context: where an event came from, and what it touched — ADR-46.
--
-- Migration 000033 deliberately refused an ip column, and the reason it gave
-- was sound: X-Forwarded-For is forgeable on a direct bind, so a column filled
-- from it would be authoritative-looking and attacker-controlled at the same
-- time — the worst combination for a table people reach for during an
-- incident. What that argument rules out is an ip column that CANNOT say where
-- its value came from. It does not rule out recording the address the server
-- actually observed, next to a flag that says whether a configured proxy
-- vouched for it.
--
-- So: ip is whatever RemoteAddr held after server.trustedProxyRealIP ran, and
-- ip_trusted is true only when that middleware rewrote it from a peer inside
-- TRUSTED_PROXY_IPS. With no proxy configured every row is the raw peer and
-- ip_trusted is false — honest, and the screen renders the difference instead
-- of hiding it.
--
-- user_agent is client-controlled text with no authority whatsoever. It is
-- kept because "same account, new device" is a question incidents actually
-- ask, and it is capped and rendered as text, never as markup.
--
-- entity_kind/entity_id/subject carry CONTENT events (ADR-46): which row a
-- link/note/folder/tag event touched, and its human label. subject is the only
-- column in this table that holds user content, and it is projected by exactly
-- one query — the owner's own-activity feed. The administrative trail selects
-- the other columns and never this one, which is what keeps INV-045 true while
-- the same row serves both readers.
ALTER TABLE audit_log
    ADD COLUMN ip          INET,
    ADD COLUMN ip_trusted  BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN user_agent  TEXT,
    ADD COLUMN entity_kind TEXT,
    ADD COLUMN entity_id   BIGINT,
    ADD COLUMN subject     TEXT;

-- Caps for the same reason 000033 capped actor_email: the failed-login writer
-- accepts a row from any unauthenticated caller who can reach the port, and
-- User-Agent is a request header they control completely. Without a cap one
-- attempt writes an arbitrarily large permanent row, and the per-address rate
-- bucket cannot help — it is keyed by the attempted address, so a fresh string
-- buys a fresh budget.
ALTER TABLE audit_log
    ADD CONSTRAINT audit_log_user_agent_len_chk  CHECK (user_agent IS NULL OR length(user_agent) <= 256),
    ADD CONSTRAINT audit_log_entity_kind_len_chk CHECK (entity_kind IS NULL OR length(entity_kind) <= 32),
    ADD CONSTRAINT audit_log_subject_len_chk     CHECK (subject IS NULL OR length(subject) <= 256);

-- The owner's own-activity feed reads "my rows, newest first". Without this it
-- is a backward scan of the whole trail filtered by actor — and the trail is
-- dominated by rows belonging to nobody (failed logins), so the filter would
-- discard almost everything it read.
CREATE INDEX audit_log_actor_idx ON audit_log (actor_id, id DESC) WHERE actor_id IS NOT NULL;

-- There is deliberately NO index on ip. Every query that touches the column
-- filters on created_at and GROUPS BY ip — none of them has an equality on ip,
-- so a (ip, created_at) index would never be a range start, and the planner
-- would take audit_log_created_idx for the window and hash-aggregate anyway.
-- The one place ip appears in a WHERE is the search, as host(ip) LIKE, which is
-- a function call and not sargable on any index of the raw column. On the
-- most-written table in the instance an index nothing reads is write
-- amplification and disk for nothing.

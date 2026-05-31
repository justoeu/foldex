-- 000010_link_change_check.up.sql
--
-- Per-link opt-in para detecção periódica de alterações.
--
--   check_interval IS NULL                       → desativado (default em todos os existentes)
--   check_interval IN ('hourly','daily','weekly') → o changecheck worker
--       processa o link nesse intervalo e dispara push quando o fingerprint
--       diverge do `last_fingerprint`.
--
-- Semântica das colunas:
--   last_checked_at         → bumped a cada execução do worker (sucesso ou falha)
--   last_fingerprint        → "feed:<sha256>" OU "content:<sha256>" — prefixo
--                              indica a estratégia usada, evita repensar
--                              fingerprints antigos quando uma página ganha
--                              feed RSS no meio do caminho
--   last_change_detected_at → bumped APENAS quando o fingerprint mudou de um
--                              valor não-nulo (primeira detecção NUNCA conta
--                              como mudança, senão todo opt-in viraria push)
--   change_seen_at          → bumped via POST /api/links/{id}/seen-change
--                              quando o usuário clica no badge. Critério de
--                              "tem update novo" no UI é:
--                                  last_change_detected_at IS NOT NULL
--                                  AND (change_seen_at IS NULL
--                                       OR change_seen_at < last_change_detected_at)

ALTER TABLE link
    ADD COLUMN check_interval          TEXT,
    ADD COLUMN last_checked_at         TIMESTAMPTZ,
    ADD COLUMN last_fingerprint        TEXT,
    ADD COLUMN last_change_detected_at TIMESTAMPTZ,
    ADD COLUMN change_seen_at          TIMESTAMPTZ,
    -- last_check_error keeps changecheck failure messages OUT of preview_error.
    -- The preview worker owns preview_status/preview_error (CLAUDE.md §4
    -- "Worker is the only writer"); overloading that column from a sibling
    -- worker would cross domain boundaries and confuse LinkCard's
    -- preview-failure surface. Kept TEXT NULL — empty/NULL = last scan was
    -- clean, non-null = most recent error message.
    ADD COLUMN last_check_error        TEXT;

ALTER TABLE link
    ADD CONSTRAINT link_check_interval_valid
        CHECK (check_interval IS NULL OR check_interval IN ('hourly', 'daily', 'weekly'));

-- Index parcial: só linhas opt-in entram no plano do scanner. Para 100k links
-- com 10 opt-in, o index tem 10 entries — o scan do worker é O(opt-in), não
-- O(total).
CREATE INDEX link_check_due_idx
    ON link (check_interval, last_checked_at)
    WHERE check_interval IS NOT NULL;

-- Index para a query da sidebar "Atualizações recentes" (últimos 7 dias,
-- ordenado por last_change_detected_at DESC).
CREATE INDEX link_change_recent_idx
    ON link (last_change_detected_at DESC)
    WHERE last_change_detected_at IS NOT NULL;

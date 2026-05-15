-- 000009_link_slug.down.sql
--
-- Reverses 000009. The unaccent extension stays — other migrations may
-- start using it later and dropping is risky on shared DBs.

DROP INDEX IF EXISTS link_slug_idx;
ALTER TABLE link DROP CONSTRAINT IF EXISTS link_slug_format;
ALTER TABLE link DROP CONSTRAINT IF EXISTS link_slug_unique;
ALTER TABLE link DROP COLUMN IF EXISTS slug;

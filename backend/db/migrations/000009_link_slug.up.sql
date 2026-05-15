-- 000009_link_slug.up.sql
--
-- Adds `link.slug TEXT NOT NULL UNIQUE` so /go/{slug} can resolve as an
-- alternative to /go/{id} (ADR-7 in docs/ARCHITECTURE.md). Backfills every
-- existing row with a slug derived from `title` so no link is left without
-- one. Backward-compat is preserved: the redirect handler tries the int-ID
-- path first, then falls back to slug lookup.
--
-- The CHECK constraint pins the format to lowercase ASCII alphanumeric +
-- hyphens, no leading/trailing/consecutive hyphens, and explicitly rejects
-- pure-numeric slugs (e.g. "42") so they can't shadow the ID path.

CREATE EXTENSION IF NOT EXISTS unaccent;

ALTER TABLE link ADD COLUMN slug TEXT;

-- Backfill: slugify(title) with `link-{id}` fallback when title produces an
-- empty slug (rare — only happens if title was null/whitespace/punctuation).
-- Collisions are deduped via row_number — the first row keeps the bare slug,
-- subsequent rows get "-2", "-3", … suffixes. Capped at 80 chars on a
-- hyphen boundary to match the DB CHECK + Go Slugify().
WITH slugified AS (
    SELECT id,
           CASE
               WHEN COALESCE(
                       NULLIF(
                           regexp_replace(
                               trim(
                                   both '-' from
                                   regexp_replace(
                                       lower(unaccent(coalesce(title, ''))),
                                       '[^a-z0-9]+', '-', 'g'
                                   )
                               ),
                               '-+', '-', 'g'
                           ),
                           ''
                       ),
                       'link-' || id::text
                   ) ~ '^[0-9]+$'
               THEN 'link-' || COALESCE(
                       NULLIF(
                           regexp_replace(
                               trim(
                                   both '-' from
                                   regexp_replace(
                                       lower(unaccent(coalesce(title, ''))),
                                       '[^a-z0-9]+', '-', 'g'
                                   )
                               ),
                               '-+', '-', 'g'
                           ),
                           ''
                       ),
                       id::text
                   )
               ELSE COALESCE(
                       NULLIF(
                           regexp_replace(
                               trim(
                                   both '-' from
                                   regexp_replace(
                                       lower(unaccent(coalesce(title, ''))),
                                       '[^a-z0-9]+', '-', 'g'
                                   )
                               ),
                               '-+', '-', 'g'
                           ),
                           ''
                       ),
                       'link-' || id::text
                   )
           END AS base
    FROM link
),
capped AS (
    SELECT id,
           CASE
               WHEN length(base) <= 80 THEN base
               -- Truncate on a hyphen boundary so we never split a word.
               ELSE rtrim(
                   substr(base, 1,
                       GREATEST(
                           1,
                           length(substring(substr(base, 1, 80) from '^(.*)-[^-]*$'))
                       )
                   ),
                   '-'
               )
           END AS base
    FROM slugified
),
ranked AS (
    SELECT id, base,
           row_number() OVER (PARTITION BY base ORDER BY id) AS dup_rank
    FROM capped
)
UPDATE link l
SET slug = CASE
    WHEN r.dup_rank = 1 THEN r.base
    ELSE r.base || '-' || r.dup_rank::text
END
FROM ranked r
WHERE l.id = r.id;

ALTER TABLE link ALTER COLUMN slug SET NOT NULL;
ALTER TABLE link ADD CONSTRAINT link_slug_unique UNIQUE (slug);
ALTER TABLE link ADD CONSTRAINT link_slug_format
    CHECK (slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$' AND slug !~ '^[0-9]+$');
CREATE INDEX link_slug_idx ON link (slug);

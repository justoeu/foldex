-- 000029_slug_length
--
-- Slug collision suffixes historically extended an already-capped 80-byte
-- base. Repair those legacy rows without losing uniqueness, then make the
-- application limit a database invariant for both public slug namespaces.

LOCK TABLE link, note IN SHARE ROW EXCLUSIVE MODE;

DO $$
DECLARE
    table_name TEXT;
    legacy RECORD;
    attempt INTEGER;
    suffix TEXT;
    stem TEXT;
    candidate TEXT;
    stem_limit INTEGER;
    occupied BOOLEAN;
BEGIN
    FOREACH table_name IN ARRAY ARRAY['link', 'note']
    LOOP
        FOR legacy IN EXECUTE format(
            'SELECT id, slug FROM %I WHERE octet_length(slug) > 80 ORDER BY id',
            table_name
        )
        LOOP
            attempt := 1;
            LOOP
                suffix := CASE WHEN attempt = 1 THEN '' ELSE '-' || attempt::text END;
                stem := legacy.slug;
                stem_limit := 80 - octet_length(suffix);
                IF octet_length(stem) > stem_limit THEN
                    IF substring(stem FROM (stem_limit + 1) FOR 1) = '-' THEN
                        stem := left(stem, stem_limit);
                    ELSE
                        stem := left(stem, stem_limit);
                        stem := regexp_replace(stem, '-[^-]*$', '');
                    END IF;
                    stem := btrim(stem, '-');
                END IF;
                candidate := stem || suffix;
                IF candidate ~ '^[0-9]+$' THEN
                    candidate := left(candidate, 78) || '-x';
                END IF;
                IF candidate !~ '^[a-z0-9]+(-[a-z0-9]+)*$' OR candidate ~ '^[0-9]+$' THEN
                    RAISE EXCEPTION 'could not produce a valid repaired %.slug for id %', table_name, legacy.id;
                END IF;

                EXECUTE format(
                    'SELECT EXISTS (SELECT 1 FROM %I WHERE slug = $1 AND id <> $2)',
                    table_name
                ) INTO occupied USING candidate, legacy.id;
                EXIT WHEN NOT occupied;

                attempt := attempt + 1;
                IF attempt >= 1000 THEN
                    RAISE EXCEPTION 'could not repair overlong %.slug for id %', table_name, legacy.id;
                END IF;
            END LOOP;

            EXECUTE format('UPDATE %I SET slug = $1 WHERE id = $2', table_name)
                USING candidate, legacy.id;
        END LOOP;
    END LOOP;
END $$;

ALTER TABLE link ADD CONSTRAINT link_slug_length
    CHECK (octet_length(slug) <= 80);
ALTER TABLE note ADD CONSTRAINT note_slug_length
    CHECK (octet_length(slug) <= 80);

-- 000029_slug_length (down)
-- Repaired slug values stay shortened; their previous values cannot be
-- reconstructed safely after collisions have been reallocated.

ALTER TABLE note DROP CONSTRAINT note_slug_length;
ALTER TABLE link DROP CONSTRAINT link_slug_length;

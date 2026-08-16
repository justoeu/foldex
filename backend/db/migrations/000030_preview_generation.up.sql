-- A preview refresh starts a new worker generation. Result writes compare this
-- token so an older fetch/capture cannot publish into a later pending refresh.
ALTER TABLE link
  ADD COLUMN preview_generation BIGINT NOT NULL DEFAULT 1
  CHECK (preview_generation > 0);

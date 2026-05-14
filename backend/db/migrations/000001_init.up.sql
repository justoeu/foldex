CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE TABLE tag (
  id         BIGSERIAL PRIMARY KEY,
  name       TEXT NOT NULL UNIQUE,
  color      TEXT NOT NULL DEFAULT '#6366F1',
  icon       TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE link (
  id              BIGSERIAL PRIMARY KEY,
  url             TEXT NOT NULL,
  title           TEXT NOT NULL,
  description     TEXT,
  favicon_url     TEXT,
  og_image_url    TEXT,
  click_count     BIGINT NOT NULL DEFAULT 0,
  preview_status  TEXT NOT NULL DEFAULT 'pending',
  preview_error   TEXT,
  last_clicked_at TIMESTAMPTZ,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX link_title_trgm ON link USING gin (title gin_trgm_ops);
CREATE INDEX link_url_trgm   ON link USING gin (url   gin_trgm_ops);
CREATE INDEX link_created    ON link (created_at DESC);

CREATE TABLE link_tag (
  link_id BIGINT NOT NULL REFERENCES link(id) ON DELETE CASCADE,
  tag_id  BIGINT NOT NULL REFERENCES tag(id)  ON DELETE CASCADE,
  PRIMARY KEY (link_id, tag_id)
);
CREATE INDEX link_tag_tag ON link_tag (tag_id);

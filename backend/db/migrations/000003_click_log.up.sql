-- Per-click history. Each /go/:id hit appends a row; aggregations (daily
-- chart, MoM, YoY) read from this table. link.click_count stays as a fast
-- denormalized counter, but it's authoritative only relative to its own row.
--
-- The row is intentionally narrow — no IP / user-agent / referrer — because
-- foldex is single-user and we don't want a privacy footgun by accident.

CREATE TABLE click_log (
  id         BIGSERIAL PRIMARY KEY,
  link_id    BIGINT NOT NULL REFERENCES link(id) ON DELETE CASCADE,
  clicked_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX click_log_clicked_at ON click_log (clicked_at DESC);
CREATE INDEX click_log_link_id_ts ON click_log (link_id, clicked_at DESC);

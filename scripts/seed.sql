-- Sample data for local development.
-- Run after migrate-up:    make seed
-- Safe to re-run: every INSERT is guarded by ON CONFLICT.

INSERT INTO tag (name, color, icon) VALUES
  ('jira',       '#1f6feb', '🪲'),
  ('dashboard',  '#22D3EE', '📊'),
  ('docs',       '#A78BFA', '📚'),
  ('work',       '#F59E0B', '💼')
ON CONFLICT (name) DO NOTHING;

WITH new_links AS (
  INSERT INTO link (url, title, description) VALUES
    ('https://news.ycombinator.com',                'Hacker News',                  'Tech news aggregator.'),
    ('https://github.com/anthropics/claude-code',   'Claude Code',                  'Anthropic''s CLI for Claude.'),
    ('https://jira.example.com/browse/INV-1',       'INV-1: Refactor issuing flow', 'Sample Jira issue.'),
    ('https://app.datadoghq.com/dashboard/abc',     'Issuing Gateway dashboard',    'Throughput and latency.'),
    ('https://wiki.example.com/sefaz-guide',        'SEFAZ NFe & CTe — Guia',        'Internal SEFAZ playbook.')
  RETURNING id, url
)
INSERT INTO link_tag (link_id, tag_id)
SELECT nl.id, t.id
FROM new_links nl
JOIN tag t ON t.name = CASE
  WHEN nl.url LIKE '%jira%'        THEN 'jira'
  WHEN nl.url LIKE '%datadoghq%'   THEN 'dashboard'
  WHEN nl.url LIKE '%wiki%'        THEN 'docs'
  WHEN nl.url LIKE '%github%'      THEN 'docs'
  ELSE 'work'
END
ON CONFLICT DO NOTHING;

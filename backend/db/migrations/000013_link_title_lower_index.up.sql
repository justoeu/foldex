-- Functional index for alpha/alpha_desc sort so the planner can serve
-- ORDER BY lower(title) from the index without a full-table sort. A B-tree
-- on lower(title) is cheap (<1 MB for 100k rows) and turns alpha sort into
-- a zero-sort index scan.
CREATE INDEX link_title_lower_idx ON link (lower(title));

-- Removes every grant of the permission this migration introduced — including
-- rows the matrix screen added for editor/viewer after the seed, because a
-- binary rolled back below 000041 no longer knows the permission exists.
DELETE FROM role_permission WHERE permission = 'instance.backup';

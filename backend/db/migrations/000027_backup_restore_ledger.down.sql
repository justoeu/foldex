-- Removes durable backup-restore checkpoints and their old-to-new mappings.
DROP TABLE IF EXISTS backup_restore_file;
DROP TABLE IF EXISTS backup_restore_entity;
DROP TABLE IF EXISTS backup_restore;

ALTER TABLE link
    ADD CONSTRAINT link_preview_status_check
        CHECK (preview_status IN ('pending', 'ok', 'failed'));

ALTER TABLE link
    ADD CONSTRAINT link_url_unique UNIQUE (url);

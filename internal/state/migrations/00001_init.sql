-- +goose Up
-- Initial schema: processed bookmarks + label inventory.
--
-- processed_bookmarks: one row per bookmark we've classified. The
-- primary key is the Readeck bookmark ID. applied_labels stores the
-- normalised (lower-cased, trimmed, deduped) list of labels we
-- applied at classification time.
--
-- label_inventory: every label the LLM has ever produced, with a
-- use count and first-seen timestamp. This drives the "prefer
-- existing labels" hint in the classifier prompt.

CREATE TABLE processed_bookmarks (
    bookmark_id    TEXT    PRIMARY KEY,
    classified_at  TEXT    NOT NULL,
    applied_labels TEXT    NOT NULL,    -- JSON array
    llm_model      TEXT    NOT NULL,
    confidence     REAL    NOT NULL DEFAULT 0
);

CREATE INDEX idx_processed_bookmarks_classified_at
    ON processed_bookmarks (classified_at);

CREATE TABLE label_inventory (
    name        TEXT    PRIMARY KEY,
    first_seen  TEXT    NOT NULL,
    use_count   INTEGER NOT NULL DEFAULT 1
);

CREATE INDEX idx_label_inventory_use_count
    ON label_inventory (use_count DESC);

-- +goose Down
DROP INDEX IF EXISTS idx_label_inventory_use_count;
DROP INDEX IF EXISTS idx_processed_bookmarks_classified_at;
DROP TABLE IF EXISTS label_inventory;
DROP TABLE IF EXISTS processed_bookmarks;

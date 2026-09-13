-- SPDX-License-Identifier: GPL-3.0-or-later
--
-- sqlc queries for the readeckorator state store.
--
-- These are the only schema-touching statements in the application;
-- everything else is hand-written Go on top of the generated query
-- methods. When you change the schema, also update queries.sql and
-- run `make sqlc`.

-- name: IsProcessed :one
SELECT EXISTS (
    SELECT 1
    FROM processed_bookmarks
    WHERE bookmark_id = ?
) AS processed;

-- name: GetProcessedDetail :one
SELECT
    bookmark_id,
    classified_at,
    applied_labels,
    llm_model,
    confidence
FROM processed_bookmarks
WHERE bookmark_id = ?;

-- name: UpsertProcessed :exec
INSERT INTO processed_bookmarks
    (bookmark_id, classified_at, applied_labels, llm_model, confidence)
VALUES
    (?, ?, ?, ?, ?)
ON CONFLICT (bookmark_id) DO UPDATE SET
    classified_at  = excluded.classified_at,
    applied_labels = excluded.applied_labels,
    llm_model     = excluded.llm_model,
    confidence    = excluded.confidence;

-- name: UpsertLabel :exec
INSERT INTO label_inventory (name, first_seen, use_count)
VALUES (?, ?, 1)
ON CONFLICT (name) DO UPDATE SET
    use_count = use_count + 1;

-- name: ListLabels :many
SELECT name, first_seen, use_count
FROM label_inventory
ORDER BY use_count DESC, name ASC;

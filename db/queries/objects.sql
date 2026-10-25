-- name: GetObject :one
SELECT id, workspace_id, type, created_by, created_at, updated_at, deleted_at
FROM app.objects
WHERE id = $1 AND workspace_id = $2 AND deleted_at IS NULL;

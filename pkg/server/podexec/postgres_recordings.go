package podexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresRecordingStore struct {
	pool *pgxpool.Pool
}

func NewPostgresRecordingStore(pool *pgxpool.Pool) *PostgresRecordingStore {
	return &PostgresRecordingStore{pool: pool}
}

func (store *PostgresRecordingStore) SaveRecording(ctx context.Context, recording Recording) error {
	// Expiration is enforced by reads and reclaimed opportunistically on every
	// write, so a deployment where nobody opens the history list still cannot
	// accumulate expired terminal output forever.
	_ = store.deleteExpired(ctx)
	frames, err := json.Marshal(recording.Frames)
	if err != nil {
		return fmt.Errorf("encode Pod terminal recording frames: %w", err)
	}
	if _, err := store.pool.Exec(ctx, `
INSERT INTO pod_terminal_recordings (
    id, actor_user_id, cluster_id, namespace, pod_name, pod_uid, container,
    columns, rows, started_at, ended_at, expires_at, result, exit_code,
    output_bytes, recording_bytes, truncated, frames
) VALUES (
    $1::uuid, $2::uuid, $3::uuid, $4, $5, $6, $7,
    $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18::jsonb
)
ON CONFLICT (id) DO NOTHING
`, recording.ID, recording.UserID, recording.ClusterID, recording.Namespace,
		recording.PodName, recording.PodUID, recording.Container, recording.Columns,
		recording.Rows, recording.StartedAt, recording.EndedAt, recording.ExpiresAt,
		recording.Result, recording.ExitCode, recording.OutputBytes,
		recording.RecordingBytes, recording.Truncated, frames,
	); err != nil {
		return fmt.Errorf("save Pod terminal recording: %w", err)
	}
	return nil
}

func (store *PostgresRecordingStore) ListRecordings(
	ctx context.Context,
	scope RecordingScope,
	limit int,
) ([]Recording, error) {
	if limit < 1 || limit > 100 {
		limit = 50
	}
	_ = store.deleteExpired(ctx)
	rows, err := store.pool.Query(ctx, `
SELECT id::text, actor_user_id::text, COALESCE(tenant_id::text, ''),
       COALESCE(project_id::text, ''), cluster_id::text,
       COALESCE(cluster_name, ''), namespace, pod_name,
       pod_uid, container, columns, rows, started_at, ended_at, expires_at,
       result, exit_code, output_bytes, recording_bytes, truncated
FROM pod_terminal_recordings
WHERE cluster_id = $1::uuid AND namespace = $2 AND pod_name = $3
  AND expires_at > now()
ORDER BY started_at DESC, id DESC
LIMIT $4
`, scope.ClusterID, scope.Namespace, scope.PodName, limit)
	if err != nil {
		return nil, fmt.Errorf("list Pod terminal recordings: %w", err)
	}
	defer rows.Close()
	items := make([]Recording, 0)
	for rows.Next() {
		item, scanErr := scanRecording(rows, false)
		if scanErr != nil {
			return nil, fmt.Errorf("scan Pod terminal recording: %w", scanErr)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Pod terminal recordings: %w", err)
	}
	return items, nil
}

func (store *PostgresRecordingStore) GetRecording(
	ctx context.Context,
	scope RecordingScope,
	id string,
) (Recording, error) {
	row := store.pool.QueryRow(ctx, `
SELECT id::text, actor_user_id::text, COALESCE(tenant_id::text, ''),
       COALESCE(project_id::text, ''), cluster_id::text,
       COALESCE(cluster_name, ''), namespace, pod_name,
       pod_uid, container, columns, rows, started_at, ended_at, expires_at,
       result, exit_code, output_bytes, recording_bytes, truncated, frames
FROM pod_terminal_recordings
WHERE id = $1::uuid AND cluster_id = $2::uuid AND namespace = $3 AND pod_name = $4
  AND expires_at > now()
`, id, scope.ClusterID, scope.Namespace, scope.PodName)
	item, err := scanRecording(row, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return Recording{}, ErrRecordingNotFound
	}
	if err != nil {
		return Recording{}, fmt.Errorf("get Pod terminal recording: %w", err)
	}
	return item, nil
}

type recordingScanner interface {
	Scan(...any) error
}

func scanRecording(scanner recordingScanner, withFrames bool) (Recording, error) {
	item := Recording{}
	targets := []any{
		&item.ID, &item.UserID, &item.TenantID, &item.ProjectID, &item.ClusterID,
		&item.ClusterName, &item.Namespace, &item.PodName,
		&item.PodUID, &item.Container, &item.Columns, &item.Rows, &item.StartedAt,
		&item.EndedAt, &item.ExpiresAt, &item.Result, &item.ExitCode,
		&item.OutputBytes, &item.RecordingBytes, &item.Truncated,
	}
	var frames []byte
	if withFrames {
		targets = append(targets, &frames)
	}
	if err := scanner.Scan(targets...); err != nil {
		return Recording{}, err
	}
	if withFrames && len(frames) > 0 {
		if err := json.Unmarshal(frames, &item.Frames); err != nil {
			return Recording{}, err
		}
	}
	return item, nil
}

func (store *PostgresRecordingStore) deleteExpired(ctx context.Context) error {
	cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	_, err := store.pool.Exec(cleanupContext,
		"DELETE FROM pod_terminal_recordings WHERE expires_at <= now()")
	return err
}

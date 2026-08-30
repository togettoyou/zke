package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrSavedMetricsQueryNotFound = errors.New("saved metrics query not found")
	ErrSavedMetricsQueryConflict = errors.New("saved metrics query name is already in use")
	ErrSavedMetricsQueryLimit    = errors.New("saved metrics query limit reached")
)

// MaxSavedMetricsQueriesPerProject bounds one Project's library.
//
// The Console reads the whole list in one request and renders it as a picker,
// so the ceiling is what keeps that from becoming a page of its own. It counts
// shared entries and the reader's own together for the same reason: what has to
// stay a picker is the list somebody sees, not a subset of it.
const MaxSavedMetricsQueriesPerProject = 200

// SavedMetricsQuery is one named MetricsQL expression.
//
// OwnerUserID is empty when the author's account has been deleted and the entry
// was shared, which the Project keeps. OwnerDisplayName is filled from the users
// table so a picker can say whose query it is without a second round trip.
type SavedMetricsQuery struct {
	ID               string
	ProjectID        string
	OwnerUserID      string
	OwnerDisplayName string
	Visibility       string
	Name             string
	Description      string
	Expression       string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type MetricsSavedQueryStore struct {
	pool *pgxpool.Pool
}

func NewMetricsSavedQueryStore(pool *pgxpool.Pool) *MetricsSavedQueryStore {
	return &MetricsSavedQueryStore{pool: pool}
}

const savedMetricsQueryColumns = `
    saved.id::text,
    saved.project_id::text,
    COALESCE(saved.owner_user_id::text, ''),
    COALESCE(owner.display_name, ''),
    saved.visibility,
    saved.name,
    saved.description,
    saved.expression,
    saved.created_at,
    saved.updated_at`

const savedMetricsQueryFrom = `
FROM metrics_saved_queries AS saved
LEFT JOIN users AS owner ON owner.id = saved.owner_user_id`

func scanSavedMetricsQuery(row pgx.Row) (SavedMetricsQuery, error) {
	var item SavedMetricsQuery
	err := row.Scan(
		&item.ID,
		&item.ProjectID,
		&item.OwnerUserID,
		&item.OwnerDisplayName,
		&item.Visibility,
		&item.Name,
		&item.Description,
		&item.Expression,
		&item.CreatedAt,
		&item.UpdatedAt,
	)
	return item, err
}

// ListSavedMetricsQueries returns what one reader may see in one Project: every
// entry shared into it, plus that reader's own private ones.
//
// The visibility rule is in the SQL rather than in a filter applied afterwards.
// A read that fetched the Project's rows and then dropped the ones it should not
// return would be one forgotten branch away from handing somebody else's private
// query to whoever asked.
func (store *MetricsSavedQueryStore) ListSavedMetricsQueries(
	ctx context.Context,
	projectID string,
	readerUserID string,
) ([]SavedMetricsQuery, error) {
	rows, err := store.pool.Query(ctx, `SELECT `+savedMetricsQueryColumns+savedMetricsQueryFrom+`
WHERE saved.project_id = $1
  AND (saved.visibility = 'project' OR saved.owner_user_id = $2)
ORDER BY lower(saved.name), saved.id`,
		projectID,
		readerUserID,
	)
	if err != nil {
		return nil, fmt.Errorf("list saved metrics queries: %w", err)
	}
	defer rows.Close()
	items := make([]SavedMetricsQuery, 0)
	for rows.Next() {
		item, err := scanSavedMetricsQuery(rows)
		if err != nil {
			return nil, fmt.Errorf("scan saved metrics query: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate saved metrics queries: %w", err)
	}
	return items, nil
}

// GetSavedMetricsQuery reads one row by identity and Project.
//
// The Project is part of the lookup rather than checked afterwards: the route
// authorizes a Project, and an identifier from a request must not be able to
// reach a row belonging to a different one.
func (store *MetricsSavedQueryStore) GetSavedMetricsQuery(
	ctx context.Context,
	projectID string,
	id string,
) (SavedMetricsQuery, error) {
	item, err := scanSavedMetricsQuery(store.pool.QueryRow(ctx,
		`SELECT `+savedMetricsQueryColumns+savedMetricsQueryFrom+`
WHERE saved.project_id = $1 AND saved.id = $2`,
		projectID,
		id,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return SavedMetricsQuery{}, ErrSavedMetricsQueryNotFound
	}
	if err != nil {
		return SavedMetricsQuery{}, fmt.Errorf("get saved metrics query: %w", err)
	}
	return item, nil
}

type CreateSavedMetricsQueryParams struct {
	ID          string
	ProjectID   string
	OwnerUserID string
	Visibility  string
	Name        string
	Description string
	Expression  string
	Now         time.Time
}

// CreateSavedMetricsQuery inserts one entry, refusing to take the Project past
// its ceiling.
//
// The count and the insert are one statement so two concurrent creations cannot
// both read a count below the limit and both write. It is a soft ceiling on a
// picker rather than a security boundary, but a limit that only holds when
// nobody is in a hurry is not worth writing.
func (store *MetricsSavedQueryStore) CreateSavedMetricsQuery(
	ctx context.Context,
	input CreateSavedMetricsQueryParams,
) (SavedMetricsQuery, error) {
	item, err := scanSavedMetricsQuery(store.pool.QueryRow(ctx, `
WITH inserted AS (
    INSERT INTO metrics_saved_queries (
        id, project_id, owner_user_id, visibility,
        name, description, expression, created_at, updated_at
    )
    SELECT $1, $2, $3, $4, $5, $6, $7, $8, $8
    WHERE (
        SELECT count(*) FROM metrics_saved_queries WHERE project_id = $2
    ) < $9
    RETURNING *
)
SELECT `+savedMetricsQueryColumns+`
FROM inserted AS saved
LEFT JOIN users AS owner ON owner.id = saved.owner_user_id`,
		input.ID,
		input.ProjectID,
		input.OwnerUserID,
		input.Visibility,
		input.Name,
		input.Description,
		input.Expression,
		input.Now,
		MaxSavedMetricsQueriesPerProject,
	))
	if isUniqueViolation(err) {
		return SavedMetricsQuery{}, ErrSavedMetricsQueryConflict
	}
	if errors.Is(err, pgx.ErrNoRows) {
		// The insert selected no row, which happens only when the ceiling
		// check failed.
		return SavedMetricsQuery{}, ErrSavedMetricsQueryLimit
	}
	if err != nil {
		return SavedMetricsQuery{}, fmt.Errorf("create saved metrics query: %w", err)
	}
	return item, nil
}

type UpdateSavedMetricsQueryParams struct {
	ID          string
	ProjectID   string
	Visibility  string
	Name        string
	Description string
	Expression  string
	Now         time.Time
}

// UpdateSavedMetricsQuery rewrites one entry. The owner is never changed: an
// entry shared into a Project and then edited by a metrics administrator still
// says who wrote it.
func (store *MetricsSavedQueryStore) UpdateSavedMetricsQuery(
	ctx context.Context,
	input UpdateSavedMetricsQueryParams,
) (SavedMetricsQuery, error) {
	item, err := scanSavedMetricsQuery(store.pool.QueryRow(ctx, `
WITH updated AS (
    UPDATE metrics_saved_queries
    SET visibility = $3,
        name = $4,
        description = $5,
        expression = $6,
        updated_at = $7
    WHERE id = $1 AND project_id = $2
    RETURNING *
)
SELECT `+savedMetricsQueryColumns+`
FROM updated AS saved
LEFT JOIN users AS owner ON owner.id = saved.owner_user_id`,
		input.ID,
		input.ProjectID,
		input.Visibility,
		input.Name,
		input.Description,
		input.Expression,
		input.Now,
	))
	if isUniqueViolation(err) {
		return SavedMetricsQuery{}, ErrSavedMetricsQueryConflict
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return SavedMetricsQuery{}, ErrSavedMetricsQueryNotFound
	}
	if err != nil {
		return SavedMetricsQuery{}, fmt.Errorf("update saved metrics query: %w", err)
	}
	return item, nil
}

func (store *MetricsSavedQueryStore) DeleteSavedMetricsQuery(
	ctx context.Context,
	projectID string,
	id string,
) error {
	command, err := store.pool.Exec(ctx,
		`DELETE FROM metrics_saved_queries WHERE id = $1 AND project_id = $2`,
		id,
		projectID,
	)
	if err != nil {
		return fmt.Errorf("delete saved metrics query: %w", err)
	}
	if command.RowsAffected() == 0 {
		return ErrSavedMetricsQueryNotFound
	}
	return nil
}

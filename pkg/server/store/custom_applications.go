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
	ErrCustomApplicationNotFound            = errors.New("custom application not found")
	ErrCustomApplicationConflict            = errors.New("custom application name is already in use")
	ErrCustomApplicationLimit               = errors.New("custom application limit reached")
	ErrCustomApplicationIdempotencyConflict = errors.New("idempotency key was reused with different custom application input")
)

const MaxCustomApplicationsPerProject = 100

type CustomApplication struct {
	ID              string
	ProjectID       string
	CreatedByUserID string
	Name            string
	Description     string
	URL             string
	LogoURL         string
	IdempotencyKey  string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type CustomApplicationStore struct {
	pool *pgxpool.Pool
}

func NewCustomApplicationStore(pool *pgxpool.Pool) *CustomApplicationStore {
	return &CustomApplicationStore{pool: pool}
}

const customApplicationColumns = `
    id::text,
    project_id::text,
    COALESCE(created_by_user_id::text, ''),
    name,
    description,
    url,
    logo_url,
    idempotency_key,
    created_at,
    updated_at`

func scanCustomApplication(row pgx.Row) (CustomApplication, error) {
	var item CustomApplication
	err := row.Scan(
		&item.ID,
		&item.ProjectID,
		&item.CreatedByUserID,
		&item.Name,
		&item.Description,
		&item.URL,
		&item.LogoURL,
		&item.IdempotencyKey,
		&item.CreatedAt,
		&item.UpdatedAt,
	)
	return item, err
}

func (store *CustomApplicationStore) ListCustomApplications(
	ctx context.Context,
	projectID string,
) ([]CustomApplication, error) {
	rows, err := store.pool.Query(ctx, `SELECT `+customApplicationColumns+`
FROM custom_applications
WHERE project_id = $1
ORDER BY lower(name), id`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list custom applications: %w", err)
	}
	defer rows.Close()
	items := make([]CustomApplication, 0)
	for rows.Next() {
		item, err := scanCustomApplication(rows)
		if err != nil {
			return nil, fmt.Errorf("scan custom application: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate custom applications: %w", err)
	}
	return items, nil
}

func (store *CustomApplicationStore) GetCustomApplication(
	ctx context.Context,
	projectID string,
	id string,
) (CustomApplication, error) {
	item, err := scanCustomApplication(store.pool.QueryRow(ctx, `SELECT `+customApplicationColumns+`
FROM custom_applications
WHERE project_id = $1 AND id = $2`, projectID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return CustomApplication{}, ErrCustomApplicationNotFound
	}
	if err != nil {
		return CustomApplication{}, fmt.Errorf("get custom application: %w", err)
	}
	return item, nil
}

type CreateCustomApplicationParams struct {
	ID              string
	ProjectID       string
	CreatedByUserID string
	Name            string
	Description     string
	URL             string
	LogoURL         string
	IdempotencyKey  string
	Now             time.Time
}

func (store *CustomApplicationStore) CreateCustomApplication(
	ctx context.Context,
	input CreateCustomApplicationParams,
) (CustomApplication, error) {
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return CustomApplication{}, fmt.Errorf("begin custom application create: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Serialize creates within one Project. It makes the per-Project ceiling and
	// idempotency decision one transaction even when two retries arrive together.
	if _, err := tx.Exec(ctx, `SELECT 1 FROM projects WHERE id = $1 FOR UPDATE`, input.ProjectID); err != nil {
		return CustomApplication{}, fmt.Errorf("lock custom application project: %w", err)
	}
	existing, err := scanCustomApplication(tx.QueryRow(ctx, `SELECT `+customApplicationColumns+`
FROM custom_applications
WHERE project_id = $1 AND created_by_user_id = $2 AND idempotency_key = $3`,
		input.ProjectID, input.CreatedByUserID, input.IdempotencyKey))
	if err == nil {
		if existing.Name != input.Name || existing.Description != input.Description ||
			existing.URL != input.URL || existing.LogoURL != input.LogoURL {
			return CustomApplication{}, ErrCustomApplicationIdempotencyConflict
		}
		if err := tx.Commit(ctx); err != nil {
			return CustomApplication{}, fmt.Errorf("commit custom application replay: %w", err)
		}
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return CustomApplication{}, fmt.Errorf("read custom application idempotency claim: %w", err)
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM custom_applications WHERE project_id = $1`, input.ProjectID).Scan(&count); err != nil {
		return CustomApplication{}, fmt.Errorf("count custom applications: %w", err)
	}
	if count >= MaxCustomApplicationsPerProject {
		return CustomApplication{}, ErrCustomApplicationLimit
	}
	item, err := scanCustomApplication(tx.QueryRow(ctx, `
INSERT INTO custom_applications (
    id, project_id, created_by_user_id, name, description, url, logo_url,
    idempotency_key, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9)
RETURNING `+customApplicationColumns,
		input.ID, input.ProjectID, input.CreatedByUserID, input.Name,
		input.Description, input.URL, input.LogoURL, input.IdempotencyKey, input.Now))
	if isUniqueViolation(err) {
		return CustomApplication{}, ErrCustomApplicationConflict
	}
	if err != nil {
		return CustomApplication{}, fmt.Errorf("create custom application: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return CustomApplication{}, fmt.Errorf("commit custom application create: %w", err)
	}
	return item, nil
}

type UpdateCustomApplicationParams struct {
	ID          string
	ProjectID   string
	Name        string
	Description string
	URL         string
	LogoURL     string
	Now         time.Time
}

func (store *CustomApplicationStore) UpdateCustomApplication(
	ctx context.Context,
	input UpdateCustomApplicationParams,
) (CustomApplication, error) {
	item, err := scanCustomApplication(store.pool.QueryRow(ctx, `
UPDATE custom_applications
SET name = $3, description = $4, url = $5, logo_url = $6, updated_at = $7
WHERE id = $1 AND project_id = $2
RETURNING `+customApplicationColumns,
		input.ID, input.ProjectID, input.Name, input.Description, input.URL,
		input.LogoURL, input.Now))
	if isUniqueViolation(err) {
		return CustomApplication{}, ErrCustomApplicationConflict
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return CustomApplication{}, ErrCustomApplicationNotFound
	}
	if err != nil {
		return CustomApplication{}, fmt.Errorf("update custom application: %w", err)
	}
	return item, nil
}

func (store *CustomApplicationStore) DeleteCustomApplication(
	ctx context.Context,
	projectID string,
	id string,
) error {
	command, err := store.pool.Exec(ctx,
		`DELETE FROM custom_applications WHERE id = $1 AND project_id = $2`, id, projectID)
	if err != nil {
		return fmt.Errorf("delete custom application: %w", err)
	}
	if command.RowsAffected() == 0 {
		return ErrCustomApplicationNotFound
	}
	return nil
}

package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

func (store *ResourceManagementStore) ListTenants(
	ctx context.Context,
) ([]TenantResource, error) {
	rows, err := store.pool.Query(ctx, `
SELECT
    tenant.id::text,
    tenant.name,
    tenant.status,
    tenant.created_at,
    tenant.updated_at
FROM tenants AS tenant
ORDER BY lower(tenant.name), tenant.id
`)
	if err != nil {
		return nil, fmt.Errorf("list visible tenants: %w", err)
	}
	defer rows.Close()
	var result []TenantResource
	for rows.Next() {
		var item TenantResource
		if err := rows.Scan(
			&item.ID,
			&item.Name,
			&item.Status,
			&item.CreatedAt,
			&item.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan visible tenant: %w", err)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate visible tenants: %w", err)
	}
	return result, nil
}

func (store *ResourceManagementStore) ListTenantProjects(
	ctx context.Context,
	tenantID string,
) ([]ProjectResource, error) {
	if strings.TrimSpace(tenantID) == "" {
		return nil, errors.New("project list tenant ID is required")
	}
	rows, err := store.pool.Query(ctx, `
SELECT
    project.id::text,
    project.tenant_id::text,
    project.name,
    project.status,
    project.created_at,
    project.updated_at
FROM projects AS project
WHERE project.tenant_id = $1
ORDER BY lower(project.name), project.id
`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list visible projects: %w", err)
	}
	defer rows.Close()
	var result []ProjectResource
	for rows.Next() {
		var item ProjectResource
		if err := rows.Scan(
			&item.ID,
			&item.TenantID,
			&item.Name,
			&item.Status,
			&item.CreatedAt,
			&item.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan visible project: %w", err)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate visible projects: %w", err)
	}
	return result, nil
}

func (store *ResourceManagementStore) ListProjectClusters(
	ctx context.Context,
	projectID string,
) ([]ClusterResource, error) {
	if strings.TrimSpace(projectID) == "" {
		return nil, errors.New("cluster list project ID is required")
	}
	rows, err := store.pool.Query(ctx, `
SELECT
    id::text,
    tenant_id::text,
    project_id::text,
    name,
    status,
    last_seen_at,
    created_at,
    updated_at
FROM clusters
WHERE project_id = $1
ORDER BY lower(name), id
`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list project clusters: %w", err)
	}
	defer rows.Close()
	var result []ClusterResource
	for rows.Next() {
		item, err := scanCluster(rows)
		if err != nil {
			return nil, fmt.Errorf("scan project cluster: %w", err)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate project clusters: %w", err)
	}
	return result, nil
}

func (store *ResourceManagementStore) GetCluster(
	ctx context.Context,
	clusterID string,
) (ClusterResource, error) {
	if strings.TrimSpace(clusterID) == "" {
		return ClusterResource{}, errors.New("cluster ID is required")
	}
	item, err := scanCluster(store.pool.QueryRow(ctx, `
SELECT
    id::text,
    tenant_id::text,
    project_id::text,
    name,
    status,
    last_seen_at,
    created_at,
    updated_at
FROM clusters
WHERE id = $1
`, clusterID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ClusterResource{}, ErrClusterNotFound
	}
	if err != nil {
		return ClusterResource{}, fmt.Errorf("get cluster: %w", err)
	}
	return item, nil
}

func (store *ResourceManagementStore) CreateTenant(
	ctx context.Context,
	params CreateTenantParams,
) (CreateTenantResult, error) {
	if invalidCreateTenantParams(params) {
		return CreateTenantResult{}, errors.New("tenant creation fields are required")
	}
	transaction, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return CreateTenantResult{}, fmt.Errorf("begin tenant creation: %w", err)
	}
	defer rollbackTransaction(transaction)

	var created TenantResource
	err = transaction.QueryRow(ctx, `
INSERT INTO tenants (id, name, status, created_at, updated_at)
SELECT $1, $2, 'active', $4, $4
FROM users
WHERE id = $3
  AND status = 'active'
RETURNING id::text, name, status, created_at, updated_at
`, params.ID, params.Name, params.ActorUserID, params.Now).Scan(
		&created.ID,
		&created.Name,
		&created.Status,
		&created.CreatedAt,
		&created.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return CreateTenantResult{}, ErrResourceCreationNotAllowed
	}
	if err != nil {
		return CreateTenantResult{}, fmt.Errorf("insert tenant: %w", err)
	}

	var requestID string
	err = transaction.QueryRow(ctx, `
INSERT INTO tenant_creation_requests (
    id, actor_user_id, idempotency_key, requested_name, tenant_id, created_at
)
VALUES (gen_random_uuid(), $1, $2, $3, $4, $5)
ON CONFLICT (actor_user_id, idempotency_key) DO NOTHING
RETURNING id::text
`,
		params.ActorUserID,
		params.IdempotencyKey,
		params.Name,
		created.ID,
		params.Now,
	).Scan(&requestID)
	if errors.Is(err, pgx.ErrNoRows) {
		var existing CreateTenantResult
		var requestedName string
		queryErr := transaction.QueryRow(ctx, `
SELECT
    request.requested_name,
    tenant.id::text,
    tenant.name,
    tenant.status,
    tenant.created_at,
    tenant.updated_at
FROM tenant_creation_requests AS request
JOIN tenants AS tenant ON tenant.id = request.tenant_id
WHERE request.actor_user_id = $1
  AND request.idempotency_key = $2
`,
			params.ActorUserID,
			params.IdempotencyKey,
		).Scan(
			&requestedName,
			&existing.Tenant.ID,
			&existing.Tenant.Name,
			&existing.Tenant.Status,
			&existing.Tenant.CreatedAt,
			&existing.Tenant.UpdatedAt,
		)
		if queryErr != nil {
			return CreateTenantResult{}, fmt.Errorf("load tenant creation replay: %w", queryErr)
		}
		if requestedName != params.Name {
			return CreateTenantResult{}, ErrResourceCreationConflict
		}
		existing.Replayed = true
		return existing, nil
	}
	if err != nil {
		return CreateTenantResult{}, fmt.Errorf("insert tenant creation request: %w", err)
	}
	if err := insertTenantCreatedAudit(ctx, transaction, params, created.ID); err != nil {
		return CreateTenantResult{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return CreateTenantResult{}, fmt.Errorf("commit tenant creation: %w", err)
	}
	return CreateTenantResult{Tenant: created}, nil
}

func (store *ResourceManagementStore) CreateProject(
	ctx context.Context,
	params CreateProjectParams,
) (CreateProjectResult, error) {
	if invalidCreateProjectParams(params) {
		return CreateProjectResult{}, errors.New("project creation fields are required")
	}
	transaction, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return CreateProjectResult{}, fmt.Errorf("begin project creation: %w", err)
	}
	defer rollbackTransaction(transaction)

	var created ProjectResource
	err = transaction.QueryRow(ctx, `
INSERT INTO projects (id, tenant_id, name, status, created_at, updated_at)
SELECT $1, tenant.id, $3, 'active', $5, $5
FROM tenants AS tenant
JOIN users ON users.id = $4
WHERE tenant.id = $2
  AND tenant.status = 'active'
  AND users.status = 'active'
RETURNING id::text, tenant_id::text, name, status, created_at, updated_at
`,
		params.ID,
		params.TenantID,
		params.Name,
		params.ActorUserID,
		params.Now,
	).Scan(
		&created.ID,
		&created.TenantID,
		&created.Name,
		&created.Status,
		&created.CreatedAt,
		&created.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		var tenantStatus string
		queryErr := transaction.QueryRow(
			ctx,
			"SELECT status FROM tenants WHERE id = $1",
			params.TenantID,
		).Scan(&tenantStatus)
		switch {
		case errors.Is(queryErr, pgx.ErrNoRows):
			return CreateProjectResult{}, ErrTenantNotFound
		case queryErr != nil:
			return CreateProjectResult{}, fmt.Errorf("check project tenant: %w", queryErr)
		case tenantStatus != "active":
			return CreateProjectResult{}, ErrResourceStateConflict
		default:
			return CreateProjectResult{}, ErrResourceCreationNotAllowed
		}
	}
	if err != nil {
		return CreateProjectResult{}, fmt.Errorf("insert project: %w", err)
	}

	var requestID string
	err = transaction.QueryRow(ctx, `
INSERT INTO project_creation_requests (
    id, actor_user_id, tenant_id, idempotency_key, requested_name,
    project_id, created_at
)
VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6)
ON CONFLICT (actor_user_id, tenant_id, idempotency_key) DO NOTHING
RETURNING id::text
`,
		params.ActorUserID,
		params.TenantID,
		params.IdempotencyKey,
		params.Name,
		created.ID,
		params.Now,
	).Scan(&requestID)
	if errors.Is(err, pgx.ErrNoRows) {
		var existing CreateProjectResult
		var requestedName string
		queryErr := transaction.QueryRow(ctx, `
SELECT
    request.requested_name,
    project.id::text,
    project.tenant_id::text,
    project.name,
    project.status,
    project.created_at,
    project.updated_at
FROM project_creation_requests AS request
JOIN projects AS project
  ON project.tenant_id = request.tenant_id
 AND project.id = request.project_id
WHERE request.actor_user_id = $1
  AND request.tenant_id = $2
  AND request.idempotency_key = $3
`,
			params.ActorUserID,
			params.TenantID,
			params.IdempotencyKey,
		).Scan(
			&requestedName,
			&existing.Project.ID,
			&existing.Project.TenantID,
			&existing.Project.Name,
			&existing.Project.Status,
			&existing.Project.CreatedAt,
			&existing.Project.UpdatedAt,
		)
		if queryErr != nil {
			return CreateProjectResult{}, fmt.Errorf("load project creation replay: %w", queryErr)
		}
		if requestedName != params.Name {
			return CreateProjectResult{}, ErrResourceCreationConflict
		}
		existing.Replayed = true
		return existing, nil
	}
	if err != nil {
		return CreateProjectResult{}, fmt.Errorf("insert project creation request: %w", err)
	}
	if err := insertProjectCreatedAudit(ctx, transaction, params, created.ID); err != nil {
		return CreateProjectResult{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return CreateProjectResult{}, fmt.Errorf("commit project creation: %w", err)
	}
	return CreateProjectResult{Project: created}, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanCluster(row rowScanner) (ClusterResource, error) {
	var item ClusterResource
	err := row.Scan(
		&item.ID,
		&item.TenantID,
		&item.ProjectID,
		&item.Name,
		&item.Status,
		&item.LastSeenAt,
		&item.CreatedAt,
		&item.UpdatedAt,
	)
	return item, err
}

func invalidCreateTenantParams(params CreateTenantParams) bool {
	return strings.TrimSpace(params.ID) == "" ||
		strings.TrimSpace(params.Name) == "" ||
		strings.TrimSpace(params.ActorUserID) == "" ||
		strings.TrimSpace(params.RequestID) == "" ||
		strings.TrimSpace(params.IdempotencyKey) == "" ||
		params.Now.IsZero()
}

func invalidCreateProjectParams(params CreateProjectParams) bool {
	return strings.TrimSpace(params.ID) == "" ||
		strings.TrimSpace(params.TenantID) == "" ||
		strings.TrimSpace(params.Name) == "" ||
		strings.TrimSpace(params.ActorUserID) == "" ||
		strings.TrimSpace(params.RequestID) == "" ||
		strings.TrimSpace(params.IdempotencyKey) == "" ||
		params.Now.IsZero()
}

func insertTenantCreatedAudit(
	ctx context.Context,
	transaction pgx.Tx,
	params CreateTenantParams,
	tenantID string,
) error {
	if _, err := transaction.Exec(ctx, `
INSERT INTO audit_events (
    id, actor_type, actor_user_id, scope_type, action, target_type,
    target_id, result, request_id, created_at
)
VALUES (
    gen_random_uuid(), 'user', $1, 'global', 'tenant.create', 'tenant',
    $2, 'succeeded', $3, $4
)
`, params.ActorUserID, tenantID, params.RequestID, params.Now); err != nil {
		return fmt.Errorf("audit tenant creation: %w", err)
	}
	return nil
}

func insertProjectCreatedAudit(
	ctx context.Context,
	transaction pgx.Tx,
	params CreateProjectParams,
	projectID string,
) error {
	if _, err := transaction.Exec(ctx, `
INSERT INTO audit_events (
    id, actor_type, actor_user_id, scope_type, tenant_id, action,
    target_type, target_id, result, request_id, created_at
)
VALUES (
    gen_random_uuid(), 'user', $1, 'tenant', $2, 'project.create',
    'project', $3, 'succeeded', $4, $5
)
`,
		params.ActorUserID,
		params.TenantID,
		projectID,
		params.RequestID,
		params.Now,
	); err != nil {
		return fmt.Errorf("audit project creation: %w", err)
	}
	return nil
}

package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/togettoyou/zke/pkg/server/auditaction"
)

// tenantVisibilityFilterSQL applies the caller's RBAC visibility inside the
// query. Filtering in SQL rather than after the fetch keeps the reported total
// consistent with what the user may actually see, and stops a Server from
// loading every tenant to answer one page.
const tenantVisibilityFilterSQL = `
FROM tenants AS tenant
WHERE (
    $1
    OR tenant.id = ANY($2::uuid[])
    OR tenant.id = ANY($3::uuid[])
  )
  AND ($4 = '' OR tenant.status = $4)
  AND (
    $5 = ''
    OR position($5 IN lower(tenant.name)) > 0
    OR position($5 IN tenant.id::text) > 0
  )
`

func (store *ResourceManagementStore) ListTenants(
	ctx context.Context,
	params ListTenantsParams,
) ([]TenantResource, int, error) {
	return queryPage(
		ctx,
		store.pool,
		"SELECT count(*) "+tenantVisibilityFilterSQL,
		`
SELECT
    tenant.id::text,
    tenant.name,
    tenant.status,
    tenant.created_at,
    tenant.updated_at
`+tenantVisibilityFilterSQL+`
ORDER BY lower(tenant.name), tenant.id
LIMIT $6 OFFSET $7
`,
		[]any{
			params.Visibility.Global,
			params.Visibility.TenantIDs,
			params.Visibility.ProjectTenantIDs,
			params.Status,
			params.Search,
		},
		params.Page,
		scanTenantRow,
		"visible tenants",
	)
}

func (store *ResourceManagementStore) GetTenant(
	ctx context.Context,
	tenantID string,
) (TenantResource, error) {
	item, err := scanTenant(store.pool.QueryRow(ctx, `
SELECT id::text, name, status, created_at, updated_at
FROM tenants
WHERE id = $1
`, tenantID))
	if errors.Is(err, pgx.ErrNoRows) {
		return TenantResource{}, ErrTenantNotFound
	}
	if err != nil {
		return TenantResource{}, fmt.Errorf("get tenant: %w", err)
	}
	return item, nil
}

// projectVisibilityFilterSQL mirrors the tenant filter for project scope. A
// project-scoped binding grants exactly its own project, and the composite
// foreign key guarantees a project identifier already implies its tenant.
const projectVisibilityFilterSQL = `
FROM projects AS project
WHERE project.tenant_id = $1
  AND (
    $2
    OR project.tenant_id = ANY($3::uuid[])
    OR project.id = ANY($4::uuid[])
  )
  AND ($5 = '' OR project.status = $5)
  AND (
    $6 = ''
    OR position($6 IN lower(project.name)) > 0
    OR position($6 IN project.id::text) > 0
  )
`

func (store *ResourceManagementStore) ListTenantProjects(
	ctx context.Context,
	params ListTenantProjectsParams,
) ([]ProjectResource, int, error) {
	if strings.TrimSpace(params.TenantID) == "" {
		return nil, 0, errors.New("project list tenant ID is required")
	}
	return queryPage(
		ctx,
		store.pool,
		"SELECT count(*) "+projectVisibilityFilterSQL,
		`
SELECT
    project.id::text,
    project.tenant_id::text,
    project.name,
    project.status,
    project.created_at,
    project.updated_at
`+projectVisibilityFilterSQL+`
ORDER BY lower(project.name), project.id
LIMIT $7 OFFSET $8
`,
		[]any{
			params.TenantID,
			params.Visibility.Global,
			params.Visibility.TenantIDs,
			params.Visibility.ProjectIDs,
			params.Status,
			params.Search,
		},
		params.Page,
		scanProjectRow,
		"visible projects",
	)
}

func (store *ResourceManagementStore) GetProject(
	ctx context.Context,
	projectID string,
) (ProjectResource, error) {
	item, err := scanProject(store.pool.QueryRow(ctx, `
SELECT id::text, tenant_id::text, name, status, created_at, updated_at
FROM projects
WHERE id = $1
`, projectID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ProjectResource{}, ErrProjectNotFound
	}
	if err != nil {
		return ProjectResource{}, fmt.Errorf("get project: %w", err)
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

	// `ON CONFLICT DO NOTHING` rather than letting the unique index raise.
	//
	// A taken name has to be told apart from a replayed submission, and a replay
	// arrives here as a duplicate name: the idempotency record cannot be written
	// before the Tenant it references, so the first thing a retry does is insert
	// the same name again. Letting that raise would abort the transaction before
	// the replay could be recognised, and the operator would be told the name is
	// taken by the very Tenant their retry created. Absorbing the conflict keeps
	// the transaction usable, and the three reasons this can return no row are
	// separated below.
	var created TenantResource
	err = transaction.QueryRow(ctx, `
INSERT INTO tenants (id, name, status, created_at, updated_at)
SELECT $1, $2, 'active', $4, $4
FROM users
WHERE id = $3
  AND status = 'active'
ON CONFLICT (lower(name)) DO NOTHING
RETURNING id::text, name, status, created_at, updated_at
`, params.ID, params.Name, params.ActorUserID, params.Now).Scan(
		&created.ID,
		&created.Name,
		&created.Status,
		&created.CreatedAt,
		&created.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		replayed, found, replayErr := findTenantCreationReplay(ctx, transaction, params)
		if replayErr != nil {
			return CreateTenantResult{}, replayErr
		}
		if found {
			if err := transaction.Commit(ctx); err != nil {
				return CreateTenantResult{}, fmt.Errorf("commit replayed tenant creation: %w", err)
			}
			return replayed, nil
		}
		// Not a replay, so either the name belongs to someone else or the actor
		// is no longer allowed to create anything. Only the name was tested by
		// the insert; the actor condition is what remains.
		var nameTaken bool
		if queryErr := transaction.QueryRow(
			ctx,
			"SELECT EXISTS (SELECT 1 FROM tenants WHERE lower(name) = lower($1))",
			params.Name,
		).Scan(&nameTaken); queryErr != nil {
			return CreateTenantResult{}, fmt.Errorf("check tenant name conflict: %w", queryErr)
		}
		if nameTaken {
			return CreateTenantResult{}, ErrTenantNameConflict
		}
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
		// The key is already recorded, so the Tenant it names is the answer.
		// Reached when the name differs from the first submission's, which is a
		// reused key rather than a retry and is refused inside the helper.
		existing, found, replayErr := findTenantCreationReplay(ctx, transaction, params)
		if replayErr != nil {
			return CreateTenantResult{}, replayErr
		}
		if !found {
			return CreateTenantResult{}, errors.New(
				"tenant creation idempotency key is recorded without a tenant",
			)
		}
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
ON CONFLICT (tenant_id, lower(name)) DO NOTHING
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
		// Same three-way split as Tenants, for the same reason: a retry of this
		// submission arrives as a duplicate name, so a replay has to be ruled
		// out before the name is called taken.
		replayed, found, replayErr := findProjectCreationReplay(ctx, transaction, params)
		if replayErr != nil {
			return CreateProjectResult{}, replayErr
		}
		if found {
			if err := transaction.Commit(ctx); err != nil {
				return CreateProjectResult{}, fmt.Errorf("commit replayed project creation: %w", err)
			}
			return replayed, nil
		}
		var nameTaken bool
		if queryErr := transaction.QueryRow(
			ctx,
			`SELECT EXISTS (
    SELECT 1 FROM projects WHERE tenant_id = $1 AND lower(name) = lower($2)
)`,
			params.TenantID,
			params.Name,
		).Scan(&nameTaken); queryErr != nil {
			return CreateProjectResult{}, fmt.Errorf("check project name conflict: %w", queryErr)
		}
		if nameTaken {
			return CreateProjectResult{}, ErrProjectNameConflict
		}
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
		existing, found, replayErr := findProjectCreationReplay(ctx, transaction, params)
		if replayErr != nil {
			return CreateProjectResult{}, replayErr
		}
		if !found {
			return CreateProjectResult{}, errors.New(
				"project creation idempotency key is recorded without a project",
			)
		}
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

func scanTenant(row rowScanner) (TenantResource, error) {
	var item TenantResource
	err := row.Scan(
		&item.ID,
		&item.Name,
		&item.Status,
		&item.CreatedAt,
		&item.UpdatedAt,
	)
	return item, err
}

func scanTenantRow(rows pgx.Rows) (TenantResource, error) {
	return scanTenant(rows)
}

func scanProjectRow(rows pgx.Rows) (ProjectResource, error) {
	return scanProject(rows)
}

func scanProject(row rowScanner) (ProjectResource, error) {
	var item ProjectResource
	err := row.Scan(
		&item.ID,
		&item.TenantID,
		&item.Name,
		&item.Status,
		&item.CreatedAt,
		&item.UpdatedAt,
	)
	return item, err
}

func scanCluster(row rowScanner) (ClusterResource, error) {
	var item ClusterResource
	err := row.Scan(
		&item.ID,
		&item.TenantID,
		&item.ProjectID,
		&item.Name,
		&item.AgentNamespace,
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

// findTenantCreationReplay reports the Tenant a previous submission with this
// idempotency key created. `found` is false when the key has never been used,
// which is what tells a retry apart from a name someone else already holds.
//
// A key reused for a different name is refused rather than answered: the caller
// asked for something the recorded result is not.
func findTenantCreationReplay(
	ctx context.Context,
	transaction pgx.Tx,
	params CreateTenantParams,
) (CreateTenantResult, bool, error) {
	var existing CreateTenantResult
	var requestedName string
	err := transaction.QueryRow(ctx, `
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
	if errors.Is(err, pgx.ErrNoRows) {
		return CreateTenantResult{}, false, nil
	}
	if err != nil {
		return CreateTenantResult{}, false, fmt.Errorf("load tenant creation replay: %w", err)
	}
	if requestedName != params.Name {
		return CreateTenantResult{}, false, ErrResourceCreationConflict
	}
	existing.Replayed = true
	return existing, true, nil
}

// findProjectCreationReplay is findTenantCreationReplay for Projects, whose
// idempotency key is scoped to the Tenant it creates in.
func findProjectCreationReplay(
	ctx context.Context,
	transaction pgx.Tx,
	params CreateProjectParams,
) (CreateProjectResult, bool, error) {
	var existing CreateProjectResult
	var requestedName string
	err := transaction.QueryRow(ctx, `
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
	if errors.Is(err, pgx.ErrNoRows) {
		return CreateProjectResult{}, false, nil
	}
	if err != nil {
		return CreateProjectResult{}, false, fmt.Errorf("load project creation replay: %w", err)
	}
	if requestedName != params.Name {
		return CreateProjectResult{}, false, ErrResourceCreationConflict
	}
	existing.Replayed = true
	return existing, true, nil
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
    gen_random_uuid(), 'user', $1, 'global', $2, $3,
    $4, 'succeeded', $5, $6
)
`, params.ActorUserID, auditaction.TenantCreate, auditaction.TargetTenant,
		tenantID, params.RequestID, params.Now); err != nil {
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
    gen_random_uuid(), 'user', $1, 'tenant', $2, $3,
    $4, $5, 'succeeded', $6, $7
)
`,
		params.ActorUserID,
		params.TenantID,
		auditaction.ProjectCreate,
		auditaction.TargetProject,
		projectID,
		params.RequestID,
		params.Now,
	); err != nil {
		return fmt.Errorf("audit project creation: %w", err)
	}
	return nil
}

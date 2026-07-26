package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

var ErrProjectNotFound = errors.New("project not found")

func (store *RBACStore) ListRoleBindings(
	ctx context.Context,
	userID string,
) ([]RoleBinding, error) {
	if strings.TrimSpace(userID) == "" {
		return nil, errors.New("RBAC user ID is required")
	}

	rows, err := store.pool.Query(ctx, `
SELECT
    role,
    scope_type,
    COALESCE(tenant_id::text, ''),
    COALESCE(project_id::text, '')
FROM role_bindings AS binding
JOIN users ON users.id = binding.subject_id
WHERE binding.subject_id = $1
  AND users.status = 'active'
ORDER BY scope_type, tenant_id, project_id, role
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("list user role bindings: %w", err)
	}
	defer rows.Close()

	var bindings []RoleBinding
	for rows.Next() {
		var binding RoleBinding
		if err := rows.Scan(
			&binding.Role,
			&binding.ScopeType,
			&binding.TenantID,
			&binding.ProjectID,
		); err != nil {
			return nil, fmt.Errorf("scan user role binding: %w", err)
		}
		bindings = append(bindings, binding)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate user role bindings: %w", err)
	}
	return bindings, nil
}

func (store *RBACStore) FindProjectTenant(
	ctx context.Context,
	projectID string,
) (string, error) {
	var tenantID string
	err := store.pool.QueryRow(ctx, `
SELECT tenant_id::text
FROM projects
WHERE id = $1
`, projectID).Scan(&tenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrProjectNotFound
	}
	if err != nil {
		return "", fmt.Errorf("find project authorization scope: %w", err)
	}
	return tenantID, nil
}

func (store *RBACStore) FindAgentAuthorizationScope(
	ctx context.Context,
	agentID string,
) (AgentAuthorizationScope, error) {
	var scope AgentAuthorizationScope
	err := store.pool.QueryRow(ctx, `
SELECT tenant_id::text, project_id::text
FROM agents
WHERE id = $1
`, agentID).Scan(&scope.TenantID, &scope.ProjectID)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentAuthorizationScope{}, ErrAgentNotFound
	}
	if err != nil {
		return AgentAuthorizationScope{}, fmt.Errorf(
			"find Agent authorization scope: %w",
			err,
		)
	}
	return scope, nil
}

func (store *RBACStore) FindClusterAuthorizationScope(
	ctx context.Context,
	clusterID string,
) (ClusterAuthorizationScope, error) {
	var scope ClusterAuthorizationScope
	err := store.pool.QueryRow(ctx, `
SELECT tenant_id::text, project_id::text
FROM clusters
WHERE id = $1
`, clusterID).Scan(&scope.TenantID, &scope.ProjectID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ClusterAuthorizationScope{}, ErrClusterNotFound
	}
	if err != nil {
		return ClusterAuthorizationScope{}, fmt.Errorf(
			"find cluster authorization scope: %w",
			err,
		)
	}
	return scope, nil
}

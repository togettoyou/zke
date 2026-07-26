package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

func (store *AuditStore) ListRecords(
	ctx context.Context,
	input ListAuditRecordsParams,
) ([]AuditRecord, error) {
	rows, err := store.pool.Query(ctx, `
SELECT
    id::text,
    actor_type,
    COALESCE(actor_user_id::text, ''),
    COALESCE(actor_agent_id::text, ''),
    scope_type,
    COALESCE(tenant_id::text, ''),
    COALESCE(project_id::text, ''),
    COALESCE(cluster_id::text, ''),
    action,
    target_type,
    COALESCE(target_id::text, ''),
    result,
    request_id,
    created_at
FROM audit_events
WHERE (
        $1::boolean
        OR (
            scope_type <> 'global'
            AND (
                tenant_id = ANY($2::uuid[])
                OR project_id = ANY($3::uuid[])
            )
        )
    )
  AND ($4 = '' OR actor_type = $4)
  AND ($5 = '' OR result = $5)
  AND ($6 = '' OR action = $6)
  AND ($7 = '' OR target_type = $7)
  AND ($8 = '' OR request_id = $8)
  AND ($9 = '' OR tenant_id = $9::uuid)
  AND ($10 = '' OR project_id = $10::uuid)
  AND ($11 = '' OR cluster_id = $11::uuid)
  AND (
      $12::timestamptz IS NULL
      OR (created_at, id) < ($12::timestamptz, NULLIF($13, '')::uuid)
  )
ORDER BY created_at DESC, id DESC
LIMIT $14
`,
		input.GlobalVisible,
		input.TenantIDs,
		input.ProjectIDs,
		input.ActorType,
		input.Result,
		input.Action,
		input.TargetType,
		input.RequestID,
		input.TenantID,
		input.ProjectID,
		input.ClusterID,
		input.BeforeAt,
		input.BeforeID,
		input.Limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list audit records: %w", err)
	}
	defer rows.Close()
	var result []AuditRecord
	for rows.Next() {
		var item AuditRecord
		if err := rows.Scan(
			&item.ID,
			&item.ActorType,
			&item.ActorUserID,
			&item.ActorAgentID,
			&item.ScopeType,
			&item.TenantID,
			&item.ProjectID,
			&item.ClusterID,
			&item.Action,
			&item.TargetType,
			&item.TargetID,
			&item.Result,
			&item.RequestID,
			&item.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan audit record: %w", err)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate audit records: %w", err)
	}
	return result, nil
}

func (store *AuditStore) RecordTenantEvent(
	ctx context.Context,
	input TenantAuditEvent,
) error {
	if strings.TrimSpace(input.ActorUserID) == "" ||
		strings.TrimSpace(input.TenantID) == "" ||
		strings.TrimSpace(input.Action) == "" ||
		strings.TrimSpace(input.TargetType) == "" ||
		strings.TrimSpace(input.RequestID) == "" ||
		(input.Result != "failed" && input.Result != "denied") {
		return errors.New("tenant audit event fields are invalid")
	}
	if _, err := store.pool.Exec(ctx, `
WITH tenant_scope AS (
    SELECT id AS tenant_id
    FROM tenants
    WHERE id = $2::uuid
)
INSERT INTO audit_events (
    id, actor_type, actor_user_id, scope_type, tenant_id, action,
    target_type, target_id, result, request_id
)
SELECT
    gen_random_uuid(), 'user', $1::uuid, 'tenant',
    tenant_scope.tenant_id, $3, $4, NULL::uuid, $5, $6
FROM tenant_scope
UNION ALL
SELECT
    gen_random_uuid(), 'user', $1::uuid, 'global',
    NULL::uuid, $3, $4, NULL::uuid, $5, $6
WHERE NOT EXISTS (SELECT 1 FROM tenant_scope)
`,
		input.ActorUserID,
		input.TenantID,
		input.Action,
		input.TargetType,
		input.Result,
		input.RequestID,
	); err != nil {
		return fmt.Errorf("record tenant audit event: %w", err)
	}
	return nil
}

func (store *AuditStore) RecordProjectEvent(
	ctx context.Context,
	input ProjectAuditEvent,
) error {
	if strings.TrimSpace(input.ActorUserID) == "" ||
		strings.TrimSpace(input.ProjectID) == "" ||
		strings.TrimSpace(input.Action) == "" ||
		strings.TrimSpace(input.RequestID) == "" ||
		(input.Result != "failed" && input.Result != "denied") {
		return errors.New("project audit event fields are invalid")
	}

	if _, err := store.pool.Exec(ctx, `
WITH project_scope AS (
    SELECT tenant_id, id AS project_id
    FROM projects
    WHERE id = $2::uuid
)
INSERT INTO audit_events (
    id,
    actor_type,
    actor_user_id,
    scope_type,
    tenant_id,
    project_id,
    action,
    target_type,
    target_id,
    result,
    request_id
)
SELECT
    gen_random_uuid(),
    'user',
    $1::uuid,
    'project',
    project_scope.tenant_id,
    project_scope.project_id,
    $3,
    'project',
    project_scope.project_id,
    $4,
    $5
FROM project_scope
UNION ALL
SELECT
    gen_random_uuid(),
    'user',
    $1::uuid,
    'global',
    NULL::uuid,
    NULL::uuid,
    $3,
    'project',
    $2::uuid,
    $4,
    $5
WHERE NOT EXISTS (SELECT 1 FROM project_scope)
`,
		input.ActorUserID,
		input.ProjectID,
		input.Action,
		input.Result,
		input.RequestID,
	); err != nil {
		return fmt.Errorf("record project audit event: %w", err)
	}
	return nil
}

func (store *AuditStore) RecordGlobalEvent(
	ctx context.Context,
	input GlobalAuditEvent,
) error {
	if strings.TrimSpace(input.ActorUserID) == "" ||
		strings.TrimSpace(input.Action) == "" ||
		strings.TrimSpace(input.TargetType) == "" ||
		strings.TrimSpace(input.RequestID) == "" ||
		(input.Result != "failed" && input.Result != "denied") {
		return errors.New("global audit event fields are invalid")
	}

	if _, err := store.pool.Exec(ctx, `
INSERT INTO audit_events (
    id,
    actor_type,
    actor_user_id,
    scope_type,
    action,
    target_type,
    result,
    request_id
)
VALUES (
    gen_random_uuid(),
    'user',
    $1,
    'global',
    $2,
    $3,
    $4,
    $5
)
`,
		input.ActorUserID,
		input.Action,
		input.TargetType,
		input.Result,
		input.RequestID,
	); err != nil {
		return fmt.Errorf("record global audit event: %w", err)
	}
	return nil
}

func (store *AuditStore) RecordAgentEvent(
	ctx context.Context,
	input AgentAuditEvent,
) error {
	if strings.TrimSpace(input.ActorUserID) == "" ||
		strings.TrimSpace(input.AgentID) == "" ||
		strings.TrimSpace(input.Action) == "" ||
		strings.TrimSpace(input.RequestID) == "" ||
		(input.Result != "failed" && input.Result != "denied") {
		return errors.New("Agent audit event fields are invalid")
	}
	if _, err := store.pool.Exec(ctx, `
WITH agent_scope AS (
    SELECT tenant_id, project_id, cluster_id, id AS agent_id
    FROM agents
    WHERE id = $2::uuid
)
INSERT INTO audit_events (
    id,
    actor_type,
    actor_user_id,
    scope_type,
    tenant_id,
    project_id,
    cluster_id,
    action,
    target_type,
    target_id,
    result,
    request_id
)
SELECT
    gen_random_uuid(),
    'user',
    $1::uuid,
    'cluster',
    agent_scope.tenant_id,
    agent_scope.project_id,
    agent_scope.cluster_id,
    $3,
    'agent',
    agent_scope.agent_id,
    $4,
    $5
FROM agent_scope
UNION ALL
SELECT
    gen_random_uuid(),
    'user',
    $1::uuid,
    'global',
    NULL::uuid,
    NULL::uuid,
    NULL::uuid,
    $3,
    'agent',
    $2::uuid,
    $4,
    $5
WHERE NOT EXISTS (SELECT 1 FROM agent_scope)
`,
		input.ActorUserID,
		input.AgentID,
		input.Action,
		input.Result,
		input.RequestID,
	); err != nil {
		return fmt.Errorf("record Agent audit event: %w", err)
	}
	return nil
}

func (store *AuditStore) RecordClusterEvent(
	ctx context.Context,
	input ClusterAuditEvent,
) error {
	if strings.TrimSpace(input.ActorUserID) == "" ||
		strings.TrimSpace(input.ClusterID) == "" ||
		strings.TrimSpace(input.Action) == "" ||
		strings.TrimSpace(input.RequestID) == "" ||
		(input.Result != "failed" && input.Result != "denied") {
		return errors.New("cluster audit event fields are invalid")
	}
	if _, err := store.pool.Exec(ctx, `
WITH cluster_scope AS (
    SELECT tenant_id, project_id, id AS cluster_id
    FROM clusters
    WHERE id = $2::uuid
)
INSERT INTO audit_events (
    id, actor_type, actor_user_id, scope_type, tenant_id, project_id,
    cluster_id, action, target_type, target_id, result, request_id
)
SELECT
    gen_random_uuid(), 'user', $1::uuid, 'cluster',
    cluster_scope.tenant_id, cluster_scope.project_id,
    cluster_scope.cluster_id, $3, 'cluster', cluster_scope.cluster_id, $4, $5
FROM cluster_scope
UNION ALL
SELECT
    gen_random_uuid(), 'user', $1::uuid, 'global',
    NULL::uuid, NULL::uuid, NULL::uuid,
    $3, 'cluster', $2::uuid, $4, $5
WHERE NOT EXISTS (SELECT 1 FROM cluster_scope)
`,
		input.ActorUserID,
		input.ClusterID,
		input.Action,
		input.Result,
		input.RequestID,
	); err != nil {
		return fmt.Errorf("record cluster audit event: %w", err)
	}
	return nil
}

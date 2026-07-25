package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

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

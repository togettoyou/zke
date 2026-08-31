package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/togettoyou/zke/pkg/server/auditaction"
)

const auditFilterSQL = `
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
  AND ($12::timestamptz IS NULL OR created_at >= $12::timestamptz)
  AND (COALESCE(cardinality($13::text[]), 0) = 0 OR action = ANY($13::text[]))
  AND ($14::jsonb = '{}'::jsonb OR detail @> $14::jsonb)
`

// ListRecords pages audit events with the same offset contract as every other
// ZKE list. Audit events are append-only and ordered newest first, so a page
// boundary can shift as new events arrive; the trade is accepted to keep one
// paging model across the API.
func (store *AuditStore) ListRecords(
	ctx context.Context,
	input ListAuditRecordsParams,
) ([]AuditRecord, int, error) {
	detailContains := []byte(`{}`)
	if len(input.DetailContains) > 0 {
		var err error
		detailContains, err = json.Marshal(input.DetailContains)
		if err != nil {
			return nil, 0, fmt.Errorf("encode audit detail filter: %w", err)
		}
	}
	var since any
	if !input.Since.IsZero() {
		since = input.Since
	}
	return queryPage(
		ctx,
		store.pool,
		"SELECT count(*) "+auditFilterSQL,
		`
SELECT
    id::text,
    actor_type,
    COALESCE(actor_user_id::text, ''),
    COALESCE(actor_user_name, ''),
    COALESCE(actor_agent_id::text, ''),
    scope_type,
    COALESCE(tenant_id::text, ''),
    COALESCE(tenant_name, ''),
    COALESCE(project_id::text, ''),
    COALESCE(project_name, ''),
    COALESCE(cluster_id::text, ''),
    COALESCE(cluster_name, ''),
    action,
    target_type,
    COALESCE(target_id::text, ''),
    COALESCE(target_name, ''),
    result,
    request_id,
    COALESCE(host(actor_ip), ''),
    detail,
    created_at
`+auditFilterSQL+`
ORDER BY created_at DESC, id DESC
LIMIT $15 OFFSET $16
`,
		[]any{
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
			since,
			input.Actions,
			detailContains,
		},
		input.Page,
		scanAuditRecord,
		"audit records",
	)
}

func scanAuditRecord(rows pgx.Rows) (AuditRecord, error) {
	var item AuditRecord
	var detail []byte
	err := rows.Scan(
		&item.ID,
		&item.ActorType,
		&item.ActorUserID,
		&item.ActorUserName,
		&item.ActorAgentID,
		&item.ScopeType,
		&item.TenantID,
		&item.TenantName,
		&item.ProjectID,
		&item.ProjectName,
		&item.ClusterID,
		&item.ClusterName,
		&item.Action,
		&item.TargetType,
		&item.TargetID,
		&item.TargetName,
		&item.Result,
		&item.RequestID,
		&item.ActorIP,
		&detail,
		&item.CreatedAt,
	)
	if err != nil {
		return AuditRecord{}, err
	}
	item.Detail = decodeAuditDetail(detail)
	return item, nil
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
    SELECT id AS tenant_id, name AS tenant_name
    FROM tenants
    WHERE id = $2::uuid
)
INSERT INTO audit_events (
    id, actor_type, actor_user_id, scope_type, tenant_id, tenant_name,
    action, target_type, target_id, target_name, result, request_id,
    actor_ip, detail
)
SELECT
    gen_random_uuid(), 'user', $1::uuid, 'tenant',
    tenant_scope.tenant_id,
    COALESCE(NULLIF($3, ''), tenant_scope.tenant_name),
    $4, $5, NULLIF($6, '')::uuid, NULLIF($7, ''), $8, $9,
    $10::inet, $11::jsonb
FROM tenant_scope
UNION ALL
SELECT
    gen_random_uuid(), 'user', $1::uuid, 'global',
    NULL::uuid, NULLIF($3, ''),
    $4, $5, NULLIF($6, '')::uuid, NULLIF($7, ''), $8, $9,
    $10::inet, $11::jsonb
WHERE NOT EXISTS (SELECT 1 FROM tenant_scope)
`,
		input.ActorUserID,
		input.TenantID,
		input.TenantName,
		input.Action,
		input.TargetType,
		input.TargetID,
		input.TargetName,
		input.Result,
		input.RequestID,
		auditActorIP(input.ActorIP),
		auditDetail(input.Detail),
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
		strings.TrimSpace(input.TargetType) == "" ||
		strings.TrimSpace(input.RequestID) == "" ||
		(input.Result != "succeeded" && input.Result != "failed" && input.Result != "denied") {
		return errors.New("project audit event fields are invalid")
	}

	if _, err := store.pool.Exec(ctx, `
WITH project_scope AS (
    SELECT tenant_id, id AS project_id, name AS project_name
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
    project_name,
    action,
    target_type,
    target_id,
    target_name,
    result,
    request_id,
    actor_ip,
    detail
)
SELECT
    gen_random_uuid(),
    'user',
    $1::uuid,
    'project',
    project_scope.tenant_id,
    project_scope.project_id,
    COALESCE(NULLIF($3, ''), project_scope.project_name),
    $4,
    $5,
    NULLIF($6, '')::uuid,
    NULLIF($7, ''),
    $8,
    $9,
    $10::inet,
    $11::jsonb
FROM project_scope
UNION ALL
SELECT
    gen_random_uuid(),
    'user',
    $1::uuid,
    'global',
    NULL::uuid,
    NULL::uuid,
    NULLIF($3, ''),
    $4,
    $5,
    NULLIF($6, '')::uuid,
    NULLIF($7, ''),
    $8,
    $9,
    $10::inet,
    $11::jsonb
WHERE NOT EXISTS (SELECT 1 FROM project_scope)
`,
		input.ActorUserID,
		input.ProjectID,
		input.ProjectName,
		input.Action,
		input.TargetType,
		input.TargetID,
		input.TargetName,
		input.Result,
		input.RequestID,
		auditActorIP(input.ActorIP),
		auditDetail(input.Detail),
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
    target_id,
    target_name,
    result,
    request_id,
    actor_ip,
    detail
)
VALUES (
    gen_random_uuid(),
    'user',
    $1,
    'global',
    $2,
    $3,
    NULLIF($4, '')::uuid,
    NULLIF($5, ''),
    $6,
    $7,
    $8::inet,
    $9::jsonb
)
`,
		input.ActorUserID,
		input.Action,
		input.TargetType,
		input.TargetID,
		input.TargetName,
		input.Result,
		input.RequestID,
		auditActorIP(input.ActorIP),
		auditDetail(input.Detail),
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
    request_id,
    actor_ip,
    detail
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
    $4,
    agent_scope.agent_id,
    $5,
    $6,
    $7::inet,
    $8::jsonb
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
    $4,
    $2::uuid,
    $5,
    $6,
    $7::inet,
    $8::jsonb
WHERE NOT EXISTS (SELECT 1 FROM agent_scope)
`,
		input.ActorUserID,
		input.AgentID,
		input.Action,
		auditaction.TargetAgent,
		input.Result,
		input.RequestID,
		auditActorIP(input.ActorIP),
		auditDetail(input.Detail),
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
		strings.TrimSpace(input.TargetType) == "" ||
		strings.TrimSpace(input.RequestID) == "" ||
		!validClusterAuditResult(input.Result) {
		return errors.New("cluster audit event fields are invalid")
	}
	if _, err := store.pool.Exec(ctx, `
WITH cluster_scope AS (
    SELECT tenant_id, project_id, id AS cluster_id, name AS cluster_name
    FROM clusters
    WHERE id = $2::uuid
)
INSERT INTO audit_events (
    id, actor_type, actor_user_id, scope_type, tenant_id, project_id,
    cluster_id, cluster_name, action, target_type, target_id, target_name,
    result, request_id, actor_ip, detail
)
SELECT
    gen_random_uuid(), 'user', $1::uuid, 'cluster',
    cluster_scope.tenant_id, cluster_scope.project_id,
    cluster_scope.cluster_id,
    COALESCE(NULLIF($3, ''), cluster_scope.cluster_name),
    $4, $5, NULLIF($6, '')::uuid, NULLIF($7, ''), $8, $9,
    $10::inet, $11::jsonb
FROM cluster_scope
UNION ALL
SELECT
    gen_random_uuid(), 'user', $1::uuid, 'global',
    NULL::uuid, NULL::uuid, NULL::uuid,
    NULLIF($3, ''), $4, $5, NULLIF($6, '')::uuid,
    NULLIF($7, ''), $8, $9,
    $10::inet, $11::jsonb
WHERE NOT EXISTS (SELECT 1 FROM cluster_scope)
`,
		input.ActorUserID,
		input.ClusterID,
		input.ClusterName,
		input.Action,
		input.TargetType,
		input.TargetID,
		input.TargetName,
		input.Result,
		input.RequestID,
		auditActorIP(input.ActorIP),
		auditDetail(input.Detail),
	); err != nil {
		return fmt.Errorf("record cluster audit event: %w", err)
	}
	return nil
}

func validClusterAuditResult(result string) bool {
	return result == "succeeded" || result == "failed" || result == "denied"
}

// auditActorIP normalizes a client address for the `inet` column.
//
// An address PostgreSQL would reject is dropped rather than allowed to fail the
// statement: the address arrives from a proxy header that ZKE does not control,
// and losing one field of an audit row is a far smaller loss than losing the row
// that records what happened. A host:port pair is reduced to the host, because
// that is the form gin hands back on some listeners.
func auditActorIP(value string) *string {
	address := strings.TrimSpace(value)
	if address == "" {
		return nil
	}
	if host, _, err := net.SplitHostPort(address); err == nil {
		address = host
	}
	parsed := net.ParseIP(address)
	if parsed == nil {
		return nil
	}
	normalized := parsed.String()
	return &normalized
}

// auditDetail encodes the structured reason for an event, or nil when there is
// none. An absent reason stays NULL rather than becoming `{}`, so "nothing was
// recorded" and "nothing to record" stay distinguishable in the trail.
func auditDetail(detail map[string]string) []byte {
	if len(detail) == 0 {
		return nil
	}
	encoded, err := json.Marshal(detail)
	if err != nil {
		return nil
	}
	return encoded
}

// decodeAuditDetail reads the column back, tolerating anything that is not a
// flat object. Audit reads must not fail because one historical row holds a
// shape the current code does not expect.
func decodeAuditDetail(raw []byte) map[string]string {
	if len(raw) == 0 {
		return nil
	}
	detail := make(map[string]string)
	if err := json.Unmarshal(raw, &detail); err != nil {
		return nil
	}
	if len(detail) == 0 {
		return nil
	}
	return detail
}

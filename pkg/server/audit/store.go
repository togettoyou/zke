package audit

import (
	"context"

	"github.com/togettoyou/zke/pkg/server/store"
)

// Store is the persistence surface the audit service needs.
type Store interface {
	ListRecords(ctx context.Context, input store.ListAuditRecordsParams) ([]store.AuditRecord, int, error)
	RecordGlobalEvent(ctx context.Context, input store.GlobalAuditEvent) error
	RecordTenantEvent(ctx context.Context, input store.TenantAuditEvent) error
	RecordProjectEvent(ctx context.Context, input store.ProjectAuditEvent) error
	RecordClusterEvent(ctx context.Context, input store.ClusterAuditEvent) error
	RecordAgentEvent(ctx context.Context, input store.AgentAuditEvent) error
}

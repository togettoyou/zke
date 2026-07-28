package store

import (
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/togettoyou/zke/pkg/shared/pagination"
)

type AuditStore struct {
	pool *pgxpool.Pool
}

type ProjectAuditEvent struct {
	ActorUserID string
	ProjectID   string
	ProjectName string
	Action      string
	TargetType  string
	TargetID    string
	TargetName  string
	Result      string
	RequestID   string
}

type TenantAuditEvent struct {
	ActorUserID string
	TenantID    string
	TenantName  string
	Action      string
	TargetType  string
	TargetID    string
	TargetName  string
	Result      string
	RequestID   string
}

type GlobalAuditEvent struct {
	ActorUserID string
	Action      string
	TargetType  string
	TargetID    string
	TargetName  string
	Result      string
	RequestID   string
}

type ClusterAuditEvent struct {
	ActorUserID string
	ClusterID   string
	ClusterName string
	Action      string
	TargetType  string
	TargetID    string
	TargetName  string
	Result      string
	RequestID   string
}

type AgentAuditEvent struct {
	ActorUserID string
	AgentID     string
	Action      string
	Result      string
	RequestID   string
}

// AuditRecord carries an id and a name for each subject. The ids are what to
// correlate on; the names are what keep the row readable once the subject has
// been deleted, which the ids can no longer express.
type AuditRecord struct {
	ID            string
	ActorType     string
	ActorUserID   string
	ActorUserName string
	ActorAgentID  string
	ScopeType     string
	TenantID      string
	TenantName    string
	ProjectID     string
	ProjectName   string
	ClusterID     string
	ClusterName   string
	Action        string
	TargetType    string
	TargetID      string
	TargetName    string
	Result        string
	RequestID     string
	CreatedAt     time.Time
}

type ListAuditRecordsParams struct {
	GlobalVisible bool
	TenantIDs     []string
	ProjectIDs    []string
	ActorType     string
	Result        string
	Action        string
	TargetType    string
	RequestID     string
	TenantID      string
	ProjectID     string
	ClusterID     string
	Page          pagination.Request
}

func NewAuditStore(pool *pgxpool.Pool) *AuditStore {
	return &AuditStore{pool: pool}
}

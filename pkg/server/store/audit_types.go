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
	Action      string
	Result      string
	RequestID   string
}

type TenantAuditEvent struct {
	ActorUserID string
	TenantID    string
	Action      string
	TargetType  string
	Result      string
	RequestID   string
}

type GlobalAuditEvent struct {
	ActorUserID string
	Action      string
	TargetType  string
	Result      string
	RequestID   string
}

type ClusterAuditEvent struct {
	ActorUserID string
	ClusterID   string
	Action      string
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

type AuditRecord struct {
	ID           string
	ActorType    string
	ActorUserID  string
	ActorAgentID string
	ScopeType    string
	TenantID     string
	ProjectID    string
	ClusterID    string
	Action       string
	TargetType   string
	TargetID     string
	Result       string
	RequestID    string
	CreatedAt    time.Time
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

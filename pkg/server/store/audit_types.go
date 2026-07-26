package store

import "github.com/jackc/pgx/v5/pgxpool"

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

func NewAuditStore(pool *pgxpool.Pool) *AuditStore {
	return &AuditStore{pool: pool}
}

package store

import (
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/togettoyou/zke/pkg/shared/pagination"
)

type AuditStore struct {
	pool *pgxpool.Pool
}

// ActorIP and Detail appear on every event input and are optional on all of
// them. ActorIP is the client address when the event came from an HTTP request
// a user drove, empty otherwise. Detail carries the structured reason behind
// `Result` -- which permission a denial wanted, what a failure was -- as short
// stable keys. It is typed as a string map rather than `any` on purpose: the
// column is retained far longer than what it describes, and a shape that cannot
// nest is a shape nobody can drop a request body into.
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
	ActorIP     string
	Detail      map[string]string
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
	ActorIP     string
	Detail      map[string]string
}

type GlobalAuditEvent struct {
	ActorUserID string
	Action      string
	TargetType  string
	TargetID    string
	TargetName  string
	Result      string
	RequestID   string
	ActorIP     string
	Detail      map[string]string
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
	ActorIP     string
	Detail      map[string]string
}

type AgentAuditEvent struct {
	ActorUserID string
	AgentID     string
	Action      string
	Result      string
	RequestID   string
	ActorIP     string
	Detail      map[string]string
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
	ActorIP       string
	Detail        map[string]string
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

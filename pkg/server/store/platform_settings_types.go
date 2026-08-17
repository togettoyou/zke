package store

import (
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrEndpointProfileNotFound  = errors.New("Agent endpoint profile not found")
	ErrEndpointProfileConflict  = errors.New("Agent endpoint profile conflict")
	ErrEndpointProfileInUse     = errors.New("Agent endpoint profile is in use")
	ErrPlatformSettingsConflict = errors.New("platform settings revision conflict")
	// ErrPlatformWorkloadNotFound reports an update naming a workload that has
	// no row. The Server rejects unknown names before it gets here, so this is
	// the migrations and the Server's workload registry having drifted apart —
	// worth failing on rather than silently writing nothing.
	ErrPlatformWorkloadNotFound = errors.New("platform workload settings not found")
)

type PlatformSettingsStore struct {
	pool *pgxpool.Pool
}

type AgentEndpointProfile struct {
	ID                           string
	Name                         string
	RegistrationURL              string
	QUICAddress                  string
	RegistrationCACertificatePEM string
	Enabled                      bool
	Revision                     int64
	CreatedByUserID              string
	UpdatedByUserID              string
	CreatedAt                    time.Time
	UpdatedAt                    time.Time
}

// WorkloadSettings is the part of one workload ZKE installs into a Cluster that
// an operator owns: which image runs, and how much of that Cluster it may take.
//
// The JSON names are the wire names. The shape crosses every layer unchanged —
// table row, Go value, request body — and giving each layer its own spelling of
// six fields would be five renames to maintain for no reader's benefit.
type WorkloadSettings struct {
	Image           string `json:"image"`
	ImagePullPolicy string `json:"image_pull_policy"`
	// Kubernetes quantities. An empty string is not a missing field: it means
	// the entry is left off the container, which is the only way Kubernetes has
	// to say "no limit".
	CPURequest    string `json:"cpu_request"`
	MemoryRequest string `json:"memory_request"`
	CPULimit      string `json:"cpu_limit"`
	MemoryLimit   string `json:"memory_limit"`
}

type PlatformSettings struct {
	DefaultEndpointProfileID string
	// Workloads holds every platform_workload_settings row, keyed by component
	// name. Which names must be present is the Server's declaration rather than
	// the database's — see the workload registry in the platformsettings
	// package.
	Workloads                 map[string]WorkloadSettings
	ClusterTerminalSessionTTL time.Duration
	Revision                  int64
	UpdatedByUserID           string
	UpdatedAt                 time.Time
}

type CreateAgentEndpointProfileParams struct {
	ID                           string
	Name                         string
	RegistrationURL              string
	QUICAddress                  string
	RegistrationCACertificatePEM string
	Enabled                      bool
	ActorUserID                  string
	Now                          time.Time
}

type UpdateAgentEndpointProfileParams struct {
	ID                           string
	Name                         string
	RegistrationURL              string
	QUICAddress                  string
	RegistrationCACertificatePEM string
	Enabled                      bool
	ExpectedRevision             int64
	ActorUserID                  string
	Now                          time.Time
}

// UpdatePlatformSettingsParams carries only what the update changes. Both
// value-bearing fields are optional, because the settings form is edited one
// section at a time: an update that had to carry every workload would write
// back values the operator never looked at, which is how an edit abandoned in
// one section used to be saved from another.
type UpdatePlatformSettingsParams struct {
	// Workloads names only the workloads being changed. A workload absent from
	// the map keeps its stored row.
	Workloads map[string]WorkloadSettings
	// ClusterTerminalSessionTTL is nil when this update does not touch it.
	ClusterTerminalSessionTTL *time.Duration
	ExpectedRevision          int64
	ActorUserID               string
	Now                       time.Time
}

type ReconcileDefaultEndpointParams struct {
	ReservedID       string
	ReservedName     string
	PresetProfileIDs []string
	RegistrationURL  string
	QUICAddress      string
	Now              time.Time
}

func NewPlatformSettingsStore(pool *pgxpool.Pool) *PlatformSettingsStore {
	return &PlatformSettingsStore{pool: pool}
}

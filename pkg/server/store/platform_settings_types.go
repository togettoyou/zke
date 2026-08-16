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

type PlatformSettings struct {
	DefaultEndpointProfileID        string
	AgentImage                      string
	AgentImagePullPolicy            string
	ClusterTerminalImage            string
	ClusterTerminalImagePullPolicy  string
	MetricsCollectorImage           string
	MetricsCollectorImagePullPolicy string
	MetricsCollectorCPURequest      string
	MetricsCollectorMemoryRequest   string
	MetricsCollectorCPULimit        string
	MetricsCollectorMemoryLimit     string
	// The two additional scrape targets. Flat like the collector's own fields
	// because this struct mirrors the table's columns one for one; the shape
	// the three components share is expressed where they are actually treated
	// alike, in the collector service that builds the Agent's request.
	KubeStateMetricsImage           string
	KubeStateMetricsImagePullPolicy string
	KubeStateMetricsCPURequest      string
	KubeStateMetricsMemoryRequest   string
	KubeStateMetricsCPULimit        string
	KubeStateMetricsMemoryLimit     string
	NodeExporterImage               string
	NodeExporterImagePullPolicy     string
	NodeExporterCPURequest          string
	NodeExporterMemoryRequest       string
	NodeExporterCPULimit            string
	NodeExporterMemoryLimit         string
	ClusterTerminalSessionTTL       time.Duration
	Revision                        int64
	UpdatedByUserID                 string
	UpdatedAt                       time.Time
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

type UpdatePlatformSettingsParams struct {
	AgentImage                      string
	AgentImagePullPolicy            string
	ClusterTerminalImage            string
	ClusterTerminalImagePullPolicy  string
	MetricsCollectorImage           string
	MetricsCollectorImagePullPolicy string
	MetricsCollectorCPURequest      string
	MetricsCollectorMemoryRequest   string
	MetricsCollectorCPULimit        string
	MetricsCollectorMemoryLimit     string
	KubeStateMetricsImage           string
	KubeStateMetricsImagePullPolicy string
	KubeStateMetricsCPURequest      string
	KubeStateMetricsMemoryRequest   string
	KubeStateMetricsCPULimit        string
	KubeStateMetricsMemoryLimit     string
	NodeExporterImage               string
	NodeExporterImagePullPolicy     string
	NodeExporterCPURequest          string
	NodeExporterMemoryRequest       string
	NodeExporterCPULimit            string
	NodeExporterMemoryLimit         string
	ClusterTerminalSessionTTL       time.Duration
	ExpectedRevision                int64
	ActorUserID                     string
	Now                             time.Time
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

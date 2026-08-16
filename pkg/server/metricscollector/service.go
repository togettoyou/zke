// Package metricscollector turns metrics collection on and off inside one
// Cluster.
//
// The Server decides what the collector should be — which image, how often it
// scrapes — and the Cluster's Agent does the installing. Nothing here writes
// Kubernetes objects: the Agent owns their shape, so a Server bug cannot turn
// this into a way to run arbitrary workloads in an Agent Namespace.
package metricscollector

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/validation"
)

var (
	ErrInvalidInput = errors.New("invalid metrics collector request")
	ErrDisabled     = errors.New("metrics collection is not enabled on this Server")
	// ErrUnsupported separates "this Agent is too old" from "this Agent is
	// offline", because only one of them is fixed by waiting.
	ErrUnsupported = errors.New("target Cluster Agent does not support metrics collector management")
	ErrRejected    = errors.New("target Cluster refused the metrics collector operation")
)

const (
	DefaultScrapeInterval     = 30 * time.Second
	DefaultBufferSize         = "1Gi"
	DefaultKubeletMetricsPort = 10250
)

// AgentAccess is the Agent connection surface this service needs.
type AgentAccess interface {
	RequestMetricsCollector(
		context.Context,
		string,
		*agentv1.MetricsCollectorRequest,
	) (*agentv1.MetricsCollectorResponse, error)
}

// CollectorSettings is the part of a collector install an operator owns:
// which image runs and how much of the Cluster it may take. Empty quantities
// are passed through as empty, which the Agent reads as "leave this entry off
// the container".
type CollectorSettings struct {
	Image           string
	ImagePullPolicy string
	CPURequest      string
	MemoryRequest   string
	CPULimit        string
	MemoryLimit     string
}

// SettingsSource reads the collector settings from platform settings rather
// than from the Server's configuration file: collection is enabled per Cluster
// long after the Server started, so changing them must not need a restart.
type SettingsSource interface {
	CollectorSettings(context.Context) (CollectorSettings, error)
}

type Config struct {
	ScrapeInterval     time.Duration
	BufferSize         string
	KubeletMetricsPort int
}

type Service struct {
	config   Config
	agents   AgentAccess
	settings SettingsSource
}

func NewService(
	config Config,
	agents AgentAccess,
	settings SettingsSource,
) (*Service, error) {
	if agents == nil || settings == nil {
		return nil, errors.New("metrics collector dependencies are required")
	}
	if config.ScrapeInterval <= 0 {
		config.ScrapeInterval = DefaultScrapeInterval
	}
	if config.ScrapeInterval%time.Second != 0 {
		return nil, errors.New("metrics collector scrape interval must be whole seconds")
	}
	if config.BufferSize == "" {
		config.BufferSize = DefaultBufferSize
	}
	if config.KubeletMetricsPort <= 0 {
		config.KubeletMetricsPort = DefaultKubeletMetricsPort
	}
	return &Service{config: config, agents: agents, settings: settings}, nil
}

// State is what the Console shows: whether a collector is running in the
// Cluster, and what it is.
type State struct {
	ClusterID       string
	Installed       bool
	Namespace       string
	Image           string
	DesiredReplicas int32
	ReadyReplicas   int32
	CredentialReady bool
	// DesiredImage is what an install would deploy now. Shown next to Image so
	// an operator can see that a Cluster is running an older collector than the
	// platform setting currently names.
	DesiredImage string
}

func (service *Service) Status(ctx context.Context, clusterID string) (State, error) {
	return service.exchange(
		ctx,
		clusterID,
		&agentv1.MetricsCollectorRequest{
			Action: agentv1.MetricsCollectorAction_METRICS_COLLECTOR_ACTION_STATUS,
		},
	)
}

func (service *Service) Install(ctx context.Context, clusterID string) (State, error) {
	collector, err := service.settings.CollectorSettings(ctx)
	if err != nil {
		return State{}, err
	}
	if collector.Image == "" {
		return State{}, fmt.Errorf("%w: collector image is not configured", ErrInvalidInput)
	}
	return service.exchange(
		ctx,
		clusterID,
		&agentv1.MetricsCollectorRequest{
			Action:             agentv1.MetricsCollectorAction_METRICS_COLLECTOR_ACTION_INSTALL,
			Image:              collector.Image,
			ImagePullPolicy:    collector.ImagePullPolicy,
			ScrapeInterval:     strconv.Itoa(int(service.config.ScrapeInterval/time.Second)) + "s",
			BufferSize:         service.config.BufferSize,
			KubeletMetricsPort: uint32(service.config.KubeletMetricsPort),
			CpuRequest:         collector.CPURequest,
			MemoryRequest:      collector.MemoryRequest,
			CpuLimit:           collector.CPULimit,
			MemoryLimit:        collector.MemoryLimit,
		},
	)
}

func (service *Service) Uninstall(ctx context.Context, clusterID string) (State, error) {
	return service.exchange(
		ctx,
		clusterID,
		&agentv1.MetricsCollectorRequest{
			Action: agentv1.MetricsCollectorAction_METRICS_COLLECTOR_ACTION_UNINSTALL,
		},
	)
}

func (service *Service) exchange(
	ctx context.Context,
	clusterID string,
	request *agentv1.MetricsCollectorRequest,
) (State, error) {
	if !validation.IsUUID(clusterID) {
		return State{}, fmt.Errorf("%w: Cluster identifier is invalid", ErrInvalidInput)
	}
	response, err := service.agents.RequestMetricsCollector(ctx, clusterID, request)
	if err != nil {
		return State{}, err
	}
	if response.GetResult() != agentv1.ResultCode_RESULT_CODE_OK {
		// The Agent's reason travels; its message does not. The reason is a
		// stable identifier this Server produced or Kubernetes named, while the
		// message can quote objects outside this operation.
		return State{}, fmt.Errorf("%w: %s", ErrRejected, response.GetReason())
	}
	state := response.GetState()
	desired, err := service.settings.CollectorSettings(ctx)
	if err != nil {
		return State{}, err
	}
	return State{
		ClusterID:       clusterID,
		Installed:       state.GetInstalled(),
		Namespace:       state.GetNamespace(),
		Image:           state.GetImage(),
		DesiredReplicas: state.GetDesiredReplicas(),
		ReadyReplicas:   state.GetReadyReplicas(),
		CredentialReady: state.GetCredentialReady(),
		DesiredImage:    desired.Image,
	}, nil
}

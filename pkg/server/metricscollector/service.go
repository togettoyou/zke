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
	"github.com/togettoyou/zke/pkg/server/metricsingest"
	"github.com/togettoyou/zke/pkg/shared/observability"
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

// ComponentSettings is the part of one installed workload an operator owns:
// which image runs and how much of the Cluster it may take. Empty quantities
// are passed through as empty, which the Agent reads as "leave this entry off
// the container".
type ComponentSettings struct {
	Image           string
	ImagePullPolicy string
	CPURequest      string
	MemoryRequest   string
	CPULimit        string
	MemoryLimit     string
}

// CollectorSettings is the whole bundle an install puts into a Cluster: the
// collector and the two targets it scrapes.
//
// They travel together because they are installed and removed together. A
// scrape target nobody scrapes is waste, and a scrape configuration naming a
// target that was never installed only produces failing targets — so the
// operator turns on collection once and gets a pipeline that is consistent with
// itself, rather than three switches that can disagree.
type CollectorSettings struct {
	Collector        ComponentSettings
	KubeStateMetrics ComponentSettings
	NodeExporter     ComponentSettings
}

// SettingsSource reads the collector settings from platform settings rather
// than from the Server's configuration file: collection is enabled per Cluster
// long after the Server started, so changing them must not need a restart.
type SettingsSource interface {
	CollectorSettings(context.Context) (CollectorSettings, error)
}

// IngestBudget reports what the Server's own ingest gateway knows about a
// Cluster. It is consulted here because a Cluster whose batches the Server is
// refusing looks, from the Console, exactly like a Cluster whose collector has
// stopped working — and the operator would go and restart a collector that is
// doing its job. Leaving it nil is allowed and means the Server has no budget
// state to report.
type IngestBudget interface {
	ClusterState(clusterID string) (metricsingest.ClusterState, bool)
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
	budget   IngestBudget
}

func NewService(
	config Config,
	agents AgentAccess,
	settings SettingsSource,
	budget IngestBudget,
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
	return &Service{
		config:   config,
		agents:   agents,
		settings: settings,
		budget:   budget,
	}, nil
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
	// Components carries the same shape for every workload the install put into
	// the Cluster, including the collector itself. The flat fields above stay
	// for the collector because that is what the Console has always read for it.
	Components []ComponentState
	// Throttled and the fields under it describe the Server side of the same
	// link: whether this Server is currently refusing what the collector sends,
	// and why. A refusal is never hidden — a hidden one reads as a broken
	// Cluster.
	Throttled       bool
	ThrottleReason  string
	ThrottledSince  time.Time
	LastThrottledAt time.Time
	// ActiveSeries is an estimate from a fixed-size sketch, so it must be shown
	// as approximate. Zero with MaxActiveSeries zero means this Server has no
	// budget state for the Cluster at all.
	ActiveSeries    int
	MaxActiveSeries int
}

// ComponentState is one installed workload as the Cluster reports it.
type ComponentState struct {
	Component       string
	Installed       bool
	Image           string
	DesiredImage    string
	DesiredReplicas int32
	ReadyReplicas   int32
	// UnavailableReason is set when the Cluster refused this component rather
	// than when nobody asked for it. The node metrics exporter needs host
	// namespaces and host paths, which a Namespace under a restrictive Pod
	// Security admission level refuses; that refusal is reported here and does
	// not fail the install, because the rest of the pipeline works without it.
	UnavailableReason  string
	UnavailableMessage string
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
	settings, err := service.settings.CollectorSettings(ctx)
	if err != nil {
		return State{}, err
	}
	// Every component's image is required. Installing two of three would leave
	// the collector scraping a target that is not there, which reports as a
	// broken Cluster rather than as a configuration the operator never filled in.
	for _, component := range []struct {
		settings ComponentSettings
		name     string
	}{
		{settings.Collector, "collector"},
		{settings.KubeStateMetrics, "kube-state-metrics"},
		{settings.NodeExporter, "node-exporter"},
	} {
		if component.settings.Image == "" {
			return State{}, fmt.Errorf(
				"%w: %s image is not configured",
				ErrInvalidInput,
				component.name,
			)
		}
	}
	return service.exchange(
		ctx,
		clusterID,
		&agentv1.MetricsCollectorRequest{
			Action:             agentv1.MetricsCollectorAction_METRICS_COLLECTOR_ACTION_INSTALL,
			Image:              settings.Collector.Image,
			ImagePullPolicy:    settings.Collector.ImagePullPolicy,
			ScrapeInterval:     strconv.Itoa(int(service.config.ScrapeInterval/time.Second)) + "s",
			BufferSize:         service.config.BufferSize,
			KubeletMetricsPort: uint32(service.config.KubeletMetricsPort),
			CpuRequest:         settings.Collector.CPURequest,
			MemoryRequest:      settings.Collector.MemoryRequest,
			CpuLimit:           settings.Collector.CPULimit,
			MemoryLimit:        settings.Collector.MemoryLimit,
			KubeStateMetrics:   componentRequest(settings.KubeStateMetrics),
			NodeExporter:       componentRequest(settings.NodeExporter),
		},
	)
}

// componentStates pairs what the Cluster reports with what the platform
// settings currently name, so the Console can show that a Cluster is running an
// older build than the one an install would deploy now.
//
// An Agent too old to report components at all answers with none. It is then
// reported as the collector alone, which is exactly what such an Agent
// installed — inventing entries for the other two would show an operator two
// workloads that are not in their Cluster.
func componentStates(
	state *agentv1.MetricsCollectorState,
	desired CollectorSettings,
) []ComponentState {
	desiredImages := map[string]string{
		observability.ComponentCollector:    desired.Collector.Image,
		observability.ComponentKubeState:    desired.KubeStateMetrics.Image,
		observability.ComponentNodeExporter: desired.NodeExporter.Image,
	}
	reported := state.GetComponents()
	components := make([]ComponentState, 0, len(reported))
	for _, item := range reported {
		components = append(components, ComponentState{
			Component:          item.GetComponent(),
			Installed:          item.GetInstalled(),
			Image:              item.GetImage(),
			DesiredImage:       desiredImages[item.GetComponent()],
			DesiredReplicas:    item.GetDesiredReplicas(),
			ReadyReplicas:      item.GetReadyReplicas(),
			UnavailableReason:  item.GetUnavailableReason(),
			UnavailableMessage: item.GetUnavailableMessage(),
		})
	}
	return components
}

func componentRequest(settings ComponentSettings) *agentv1.MetricsCollectorComponent {
	return &agentv1.MetricsCollectorComponent{
		Image:           settings.Image,
		ImagePullPolicy: settings.ImagePullPolicy,
		CpuRequest:      settings.CPURequest,
		MemoryRequest:   settings.MemoryRequest,
		CpuLimit:        settings.CPULimit,
		MemoryLimit:     settings.MemoryLimit,
	}
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
	result := State{
		ClusterID:       clusterID,
		Installed:       state.GetInstalled(),
		Namespace:       state.GetNamespace(),
		Image:           state.GetImage(),
		DesiredReplicas: state.GetDesiredReplicas(),
		ReadyReplicas:   state.GetReadyReplicas(),
		CredentialReady: state.GetCredentialReady(),
		DesiredImage:    desired.Collector.Image,
		Components:      componentStates(state, desired),
	}
	if service.budget != nil {
		if budget, known := service.budget.ClusterState(clusterID); known {
			result.Throttled = budget.Throttled
			result.ThrottleReason = budget.Reason
			result.ThrottledSince = budget.Since
			result.LastThrottledAt = budget.LastThrottledAt
			result.ActiveSeries = budget.ActiveSeries
			result.MaxActiveSeries = budget.MaxActiveSeries
		}
	}
	return result, nil
}

package resourcewatch

import (
	"context"
	"errors"
	"fmt"
	"strings"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/server/agentconn"
	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
	"github.com/togettoyou/zke/pkg/shared/validation"
	"k8s.io/apimachinery/pkg/fields"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
)

var (
	ErrInvalidInput           = errors.New("invalid Kubernetes Event watch input")
	ErrAgentNotConnected      = errors.New("Cluster Agent is not connected")
	ErrAgentUnsupported       = errors.New("Cluster Agent does not support Resource Watch")
	ErrRequestCapacity        = errors.New("Resource Watch request capacity is exhausted")
	ErrClusterUnauthenticated = errors.New("Kubernetes API authentication failed")
	ErrClusterAccessDenied    = errors.New("Kubernetes API access denied")
	ErrClusterUnavailable     = errors.New("Kubernetes API is unavailable")
	ErrClusterTimeout         = errors.New("Kubernetes Event watch timed out")
	ErrResourceVersionExpired = errors.New("Kubernetes Event resource version expired")
	ErrWatchClosed            = errors.New("Kubernetes Event watch closed")
	ErrUpstreamFailure        = errors.New("Kubernetes Event watch failed")
	ErrInvalidResponse        = errors.New("invalid Agent Resource Watch response")
)

type Requester interface {
	RequestResourceWatch(context.Context, string, *agentv1.ResourceWatchRequest, agentprotocol.ResourceWatchSink) (
		*agentv1.ResourceWatchResponse, *agentv1.ResourceWatchTrailer, error,
	)
}

type Service struct{ requester Requester }

type Input struct {
	ClusterID, Namespace, ResourceVersion                      string
	IncludeInitial, Follow, AllowBookmarks                     bool
	InitialLimit                                               uint32
	ResourceUID, ResourceKind, ResourceName, EventType, Reason string
	// ClusterScope asks for the Events of every Namespace in the Cluster. It is
	// set only by the cluster-wide Event route, which answers to the same
	// `cluster.event.read` the Namespace route does — that permission has always
	// been Cluster-scoped, so the wider read grants nothing the caller could not
	// already reach one Namespace at a time.
	ClusterScope bool
}

type Result struct {
	ResourceVersion        string
	InitialEventsTruncated bool
	EventsSent, BytesSent  uint64
	LastResourceVersion    string
	LimitReached           bool
}

func NewService(requester Requester) *Service { return &Service{requester: requester} }

func (service *Service) Stream(ctx context.Context, input Input, sink agentprotocol.ResourceWatchSink) (Result, error) {
	if service == nil || service.requester == nil {
		return Result{}, ErrAgentUnsupported
	}
	if sink == nil || validateInput(input) != nil {
		return Result{}, ErrInvalidInput
	}
	selector := eventFieldSelector(input)
	response, trailer, err := service.requester.RequestResourceWatch(ctx, input.ClusterID, &agentv1.ResourceWatchRequest{
		Resource:  &agentv1.GroupVersionResource{Version: "v1", Resource: "events"},
		Namespace: input.Namespace, FieldSelector: selector, ResourceVersion: input.ResourceVersion,
		ClusterEventAccess:   input.ClusterScope,
		IncludeInitialEvents: input.IncludeInitial, Follow: input.Follow,
		InitialEventLimit: input.InitialLimit, AllowWatchBookmarks: input.AllowBookmarks,
		MaxEventBytes: agentprotocol.DefaultMaxResourceWatchEventBytes,
		MaxTotalBytes: agentprotocol.DefaultMaxResourceWatchTotalBytes,
		MaxEvents:     agentprotocol.DefaultMaxResourceWatchEvents,
	}, sink)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return Result{}, ErrClusterTimeout
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			return Result{}, context.Canceled
		}
		return Result{}, requestError(err)
	}
	result := Result{}
	if response != nil {
		result.ResourceVersion = response.GetResourceVersion()
		result.InitialEventsTruncated = response.GetInitialEventsTruncated()
	}
	if err := responseError(response); err != nil {
		return result, err
	}
	if trailer != nil {
		result.EventsSent, result.BytesSent = trailer.GetEventsSent(), trailer.GetBytesSent()
		result.LastResourceVersion, result.LimitReached = trailer.GetLastResourceVersion(), trailer.GetLimitReached()
	}
	if err := trailerError(trailer); err != nil {
		return result, err
	}
	return result, nil
}

func validateInput(input Input) error {
	if !validation.IsUUID(input.ClusterID) || !validEventScope(input) ||
		(!input.IncludeInitial && !input.Follow) || input.InitialLimit == 0 || input.InitialLimit > agentprotocol.MaxResourceWatchInitialEvents ||
		len(input.ResourceVersion) > 256 {
		return ErrInvalidInput
	}
	for _, value := range []string{input.ResourceUID, input.ResourceKind, input.ResourceName, input.EventType, input.Reason} {
		if len(value) > 253 || strings.TrimSpace(value) != value {
			return ErrInvalidInput
		}
	}
	if input.EventType != "" && input.EventType != "Normal" && input.EventType != "Warning" {
		return ErrInvalidInput
	}
	return nil
}

// An empty Namespace means either the cluster-wide Event page, which asks for it
// deliberately, or a bounded snapshot of one cluster-scoped Node. The
// Namespace-scoped page keeps its Namespace, and a describe cannot turn its
// exception into a cross-Namespace follow or an unfiltered list.
func validEventScope(input Input) bool {
	if input.Namespace != "" {
		return len(k8svalidation.IsDNS1123Label(input.Namespace)) == 0 && !input.ClusterScope
	}
	if input.ClusterScope {
		return true
	}
	return input.IncludeInitial && !input.Follow &&
		input.ResourceUID != "" && input.ResourceKind == "Node"
}

func eventFieldSelector(input Input) string {
	selectors := make([]fields.Selector, 0, 5)
	for _, item := range []struct{ key, value string }{
		{"involvedObject.uid", input.ResourceUID},
		{"involvedObject.kind", input.ResourceKind},
		{"involvedObject.name", input.ResourceName},
		{"type", input.EventType},
		{"reason", input.Reason},
	} {
		if item.value != "" {
			selectors = append(selectors, fields.OneTermEqualSelector(item.key, item.value))
		}
	}
	return fields.AndSelectors(selectors...).String()
}

func requestError(err error) error {
	switch {
	case errors.Is(err, agentconn.ErrAgentNotConnected):
		return ErrAgentNotConnected
	case errors.Is(err, agentconn.ErrResourceWatchCapabilityMissing):
		return ErrAgentUnsupported
	case errors.Is(err, agentconn.ErrResourceWatchRequestExhausted):
		return ErrRequestCapacity
	case errors.Is(err, context.DeadlineExceeded):
		return ErrClusterTimeout
	case errors.Is(err, context.Canceled):
		return context.Canceled
	default:
		return fmt.Errorf("%w: %w", ErrUpstreamFailure, err)
	}
}

func responseError(response *agentv1.ResourceWatchResponse) error {
	if response == nil || response.GetResult() == agentv1.ResultCode_RESULT_CODE_UNSPECIFIED {
		return ErrInvalidResponse
	}
	return resultError(response.GetResult(), response.GetReason())
}

func trailerError(trailer *agentv1.ResourceWatchTrailer) error {
	if trailer == nil || trailer.GetResult() == agentv1.ResultCode_RESULT_CODE_UNSPECIFIED {
		return ErrInvalidResponse
	}
	return resultError(trailer.GetResult(), trailer.GetReason())
}

func resultError(result agentv1.ResultCode, reason string) error {
	switch result {
	case agentv1.ResultCode_RESULT_CODE_OK:
		return nil
	case agentv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT:
		return ErrInvalidInput
	case agentv1.ResultCode_RESULT_CODE_UNAUTHENTICATED:
		return ErrClusterUnauthenticated
	case agentv1.ResultCode_RESULT_CODE_FORBIDDEN:
		return ErrClusterAccessDenied
	case agentv1.ResultCode_RESULT_CODE_CONFLICT:
		if reason == "Expired" || reason == "Gone" {
			return ErrResourceVersionExpired
		}
		return ErrUpstreamFailure
	case agentv1.ResultCode_RESULT_CODE_RESOURCE_EXHAUSTED:
		return ErrRequestCapacity
	case agentv1.ResultCode_RESULT_CODE_UNAVAILABLE:
		if reason == "WatchClosed" {
			return ErrWatchClosed
		}
		return ErrClusterUnavailable
	case agentv1.ResultCode_RESULT_CODE_TIMEOUT:
		return ErrClusterTimeout
	case agentv1.ResultCode_RESULT_CODE_CANCELED:
		return context.Canceled
	case agentv1.ResultCode_RESULT_CODE_INTERNAL:
		return ErrUpstreamFailure
	default:
		return ErrInvalidResponse
	}
}

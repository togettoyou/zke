package podportforward

import (
	"context"
	"errors"
	"fmt"
	"time"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/server/agentconn"
	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
	"github.com/togettoyou/zke/pkg/shared/validation"
)

var (
	ErrInvalidInput           = errors.New("invalid Kubernetes Pod port-forward input")
	ErrAgentNotConnected      = errors.New("Cluster Agent is not connected")
	ErrAgentUnsupported       = errors.New("Cluster Agent does not support Pod Port Forward")
	ErrRequestCapacity        = errors.New("Pod Port Forward request capacity is exhausted")
	ErrPodNotFound            = errors.New("Kubernetes Pod not found")
	ErrPodReplaced            = errors.New("Kubernetes Pod identity changed")
	ErrClusterUnauthenticated = errors.New("Kubernetes API authentication failed")
	ErrClusterAccessDenied    = errors.New("Kubernetes API access denied")
	ErrClusterUnavailable     = errors.New("Kubernetes API is unavailable")
	ErrClusterTimeout         = errors.New("Kubernetes Pod Port Forward request timed out")
	ErrByteLimit              = errors.New("Pod port-forward byte limit reached")
	ErrUpstreamFailure        = errors.New("Kubernetes Pod Port Forward request failed")
	ErrInvalidResponse        = errors.New("invalid Agent Pod Port Forward response")
)

type Requester interface {
	RequestPodPortForward(
		context.Context,
		string,
		*agentv1.PodPortForwardRequest,
		agentprotocol.PodPortForwardPeer,
	) (*agentv1.PodPortForwardResponse, *agentv1.PodPortForwardExit, error)
}

type Session struct {
	ID, UserID, AuthSessionID             string
	ClusterID, Namespace, PodName, PodUID string
	Port                                  uint32
	CreatedAt, ExpiresAt                  time.Time
}

type Result struct {
	ClientBytes, PodBytes               uint64
	ClientLimitReached, PodLimitReached bool
}

type Service struct {
	requester Requester
}

func NewService(requester Requester) *Service {
	return &Service{requester: requester}
}

// Run opens one bounded Server-to-Agent transport for a Pod Access upstream
// connection. Browser-session limits are supplied by Pod Access and are
// independently enforced across all transports belonging to that session.
func (service *Service) Run(ctx context.Context, session Session, peer agentprotocol.PodPortForwardPeer,
	maxClientBytes, maxPodBytes uint64) (Result, error) {
	if service == nil || service.requester == nil {
		return Result{}, ErrAgentUnsupported
	}
	if ctx == nil || peer == nil || !validation.IsUUID(session.ID) || maxClientBytes == 0 || maxPodBytes == 0 ||
		maxClientBytes > agentprotocol.MaxPodPortForwardBytes || maxPodBytes > agentprotocol.MaxPodPortForwardBytes {
		return Result{}, ErrInvalidInput
	}
	response, exit, err := service.requester.RequestPodPortForward(ctx, session.ClusterID,
		&agentv1.PodPortForwardRequest{Namespace: session.Namespace, PodName: session.PodName, PodUid: session.PodUID,
			Port: session.Port, MaxClientBytes: maxClientBytes, MaxPodBytes: maxPodBytes}, peer)
	if err != nil {
		return Result{}, requestError(ctx, err)
	}
	if err := responseError(response); err != nil {
		return Result{}, err
	}
	if exit == nil || exit.GetResult() == agentv1.ResultCode_RESULT_CODE_UNSPECIFIED {
		return Result{}, ErrInvalidResponse
	}
	result := Result{ClientBytes: exit.GetClientBytes(), PodBytes: exit.GetPodBytes(), ClientLimitReached: exit.GetClientLimitReached(), PodLimitReached: exit.GetPodLimitReached()}
	if exit.GetResult() == agentv1.ResultCode_RESULT_CODE_OK {
		return result, nil
	}
	if result.ClientLimitReached || result.PodLimitReached {
		return result, ErrByteLimit
	}
	return result, resultError(exit.GetResult(), exit.GetReason())
}

func requestError(ctx context.Context, err error) error {
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded), errors.Is(err, context.DeadlineExceeded):
		return ErrClusterTimeout
	case errors.Is(ctx.Err(), context.Canceled), errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, agentconn.ErrAgentNotConnected):
		return ErrAgentNotConnected
	case errors.Is(err, agentconn.ErrPodPortForwardCapabilityMissing):
		return ErrAgentUnsupported
	case errors.Is(err, agentconn.ErrPodPortForwardRequestExhausted):
		return ErrRequestCapacity
	case errors.Is(err, agentprotocol.ErrPodPortForwardClientLimit),
		errors.Is(err, agentprotocol.ErrPodPortForwardPodLimit):
		return ErrByteLimit
	default:
		return fmt.Errorf("%w: %w", ErrUpstreamFailure, err)
	}
}

func responseError(response *agentv1.PodPortForwardResponse) error {
	if response == nil || response.GetResult() == agentv1.ResultCode_RESULT_CODE_UNSPECIFIED {
		return ErrInvalidResponse
	}
	if response.GetResult() == agentv1.ResultCode_RESULT_CODE_OK {
		return nil
	}
	return resultError(response.GetResult(), response.GetReason())
}

func resultError(result agentv1.ResultCode, reason string) error {
	switch result {
	case agentv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT:
		return ErrInvalidInput
	case agentv1.ResultCode_RESULT_CODE_UNAUTHENTICATED:
		return ErrClusterUnauthenticated
	case agentv1.ResultCode_RESULT_CODE_FORBIDDEN:
		return ErrClusterAccessDenied
	case agentv1.ResultCode_RESULT_CODE_NOT_FOUND:
		return ErrPodNotFound
	case agentv1.ResultCode_RESULT_CODE_CONFLICT:
		if reason == "PodUIDMismatch" {
			return ErrPodReplaced
		}
		return ErrUpstreamFailure
	case agentv1.ResultCode_RESULT_CODE_RESOURCE_EXHAUSTED:
		return ErrRequestCapacity
	case agentv1.ResultCode_RESULT_CODE_UNAVAILABLE:
		return ErrClusterUnavailable
	case agentv1.ResultCode_RESULT_CODE_TIMEOUT:
		return ErrClusterTimeout
	case agentv1.ResultCode_RESULT_CODE_CANCELED:
		return context.Canceled
	default:
		return ErrUpstreamFailure
	}
}

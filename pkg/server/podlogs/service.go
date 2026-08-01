package podlogs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/server/agentconn"
	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
	"github.com/togettoyou/zke/pkg/shared/validation"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
)

var (
	ErrInvalidInput           = errors.New("invalid Kubernetes Pod logs input")
	ErrAgentNotConnected      = errors.New("Cluster Agent is not connected")
	ErrAgentUnsupported       = errors.New("Cluster Agent does not support Pod logs")
	ErrRequestCapacity        = errors.New("Pod logs request capacity is exhausted")
	ErrPodNotFound            = errors.New("Kubernetes Pod not found")
	ErrPodReplaced            = errors.New("Kubernetes Pod identity changed")
	ErrClusterUnauthenticated = errors.New("Kubernetes API authentication failed")
	ErrClusterAccessDenied    = errors.New("Kubernetes API access denied")
	ErrClusterUnavailable     = errors.New("Kubernetes API is unavailable")
	ErrClusterTimeout         = errors.New("Kubernetes Pod logs request timed out")
	ErrUpstreamConflict       = errors.New("Kubernetes API Pod logs conflict")
	ErrUpstreamFailure        = errors.New("Kubernetes Pod logs request failed")
	ErrInvalidResponse        = errors.New("invalid Agent Pod logs response")
)

type Requester interface {
	RequestPodLogs(
		context.Context,
		string,
		*agentv1.PodLogsRequest,
		io.Writer,
	) (*agentv1.PodLogsResponse, *agentv1.PodLogsTrailer, error)
}

type Service struct {
	requester Requester
	maxBytes  uint64
}

type Config struct {
	MaxBytes uint64
}

type Input struct {
	ClusterID    string
	Namespace    string
	PodName      string
	PodUID       string
	Container    string
	Follow       bool
	Previous     bool
	TailLines    *int64
	SinceSeconds *int64
	Timestamps   bool
}

type Result struct {
	BytesSent    uint64
	LimitReached bool
}

func NewService(requester Requester, config Config) *Service {
	maxBytes := config.MaxBytes
	if maxBytes == 0 {
		maxBytes = agentprotocol.DefaultMaxPodLogBytes
	}
	return &Service{requester: requester, maxBytes: maxBytes}
}

func (service *Service) Stream(
	ctx context.Context,
	input Input,
	destination io.Writer,
) (Result, error) {
	if service == nil || service.requester == nil {
		return Result{}, ErrAgentUnsupported
	}
	if destination == nil || validateInput(input) != nil {
		return Result{}, ErrInvalidInput
	}
	response, trailer, err := service.requester.RequestPodLogs(
		ctx,
		input.ClusterID,
		&agentv1.PodLogsRequest{
			Namespace:    input.Namespace,
			PodName:      input.PodName,
			PodUid:       input.PodUID,
			Container:    input.Container,
			Follow:       input.Follow,
			Previous:     input.Previous,
			TailLines:    input.TailLines,
			SinceSeconds: input.SinceSeconds,
			Timestamps:   input.Timestamps,
			MaxBytes:     service.maxBytes,
		},
		destination,
	)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return Result{}, ErrClusterTimeout
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			return Result{}, context.Canceled
		}
		return Result{}, requestError(err)
	}
	if err := responseError(response); err != nil {
		return Result{}, err
	}
	if err := trailerError(trailer); err != nil {
		return Result{
			BytesSent: trailer.GetBytesSent(),
		}, err
	}
	return Result{
		BytesSent:    trailer.GetBytesSent(),
		LimitReached: trailer.GetLimitReached(),
	}, nil
}

func validateInput(input Input) error {
	if !validation.IsUUID(input.ClusterID) ||
		len(k8svalidation.IsDNS1123Label(input.Namespace)) != 0 ||
		len(k8svalidation.IsDNS1123Subdomain(input.PodName)) != 0 ||
		len(k8svalidation.IsDNS1123Label(input.Container)) != 0 ||
		input.PodUID == "" || len(input.PodUID) > 256 ||
		strings.TrimSpace(input.PodUID) != input.PodUID {
		return ErrInvalidInput
	}
	if input.TailLines != nil &&
		(*input.TailLines < 0 || *input.TailLines > agentprotocol.MaxPodLogTailLines) {
		return ErrInvalidInput
	}
	if input.SinceSeconds != nil &&
		(*input.SinceSeconds < 1 || *input.SinceSeconds > agentprotocol.MaxPodLogSinceSeconds) {
		return ErrInvalidInput
	}
	return nil
}

func requestError(err error) error {
	switch {
	case errors.Is(err, agentconn.ErrAgentNotConnected):
		return ErrAgentNotConnected
	case errors.Is(err, agentconn.ErrPodLogsCapabilityMissing):
		return ErrAgentUnsupported
	case errors.Is(err, agentconn.ErrPodLogsRequestExhausted):
		return ErrRequestCapacity
	case errors.Is(err, context.DeadlineExceeded):
		return ErrClusterTimeout
	case errors.Is(err, context.Canceled):
		return context.Canceled
	default:
		return fmt.Errorf("%w: %w", ErrUpstreamFailure, err)
	}
}

func responseError(response *agentv1.PodLogsResponse) error {
	if response == nil || response.GetResult() == agentv1.ResultCode_RESULT_CODE_UNSPECIFIED {
		return ErrInvalidResponse
	}
	if response.GetResult() == agentv1.ResultCode_RESULT_CODE_OK {
		return nil
	}
	switch response.GetResult() {
	case agentv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT:
		return ErrInvalidInput
	case agentv1.ResultCode_RESULT_CODE_UNAUTHENTICATED:
		return ErrClusterUnauthenticated
	case agentv1.ResultCode_RESULT_CODE_FORBIDDEN:
		return ErrClusterAccessDenied
	case agentv1.ResultCode_RESULT_CODE_NOT_FOUND:
		return ErrPodNotFound
	case agentv1.ResultCode_RESULT_CODE_CONFLICT:
		if response.GetReason() == "PodUIDMismatch" {
			return ErrPodReplaced
		}
		return ErrUpstreamConflict
	case agentv1.ResultCode_RESULT_CODE_RESOURCE_EXHAUSTED:
		return ErrRequestCapacity
	case agentv1.ResultCode_RESULT_CODE_UNAVAILABLE:
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

func trailerError(trailer *agentv1.PodLogsTrailer) error {
	if trailer == nil || trailer.GetResult() == agentv1.ResultCode_RESULT_CODE_UNSPECIFIED {
		return ErrInvalidResponse
	}
	switch trailer.GetResult() {
	case agentv1.ResultCode_RESULT_CODE_OK:
		return nil
	case agentv1.ResultCode_RESULT_CODE_TIMEOUT:
		return ErrClusterTimeout
	case agentv1.ResultCode_RESULT_CODE_CANCELED:
		return context.Canceled
	case agentv1.ResultCode_RESULT_CODE_UNAVAILABLE:
		return ErrClusterUnavailable
	default:
		return ErrUpstreamFailure
	}
}

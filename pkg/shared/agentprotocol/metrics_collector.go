package agentprotocol

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/quic-go/quic-go"
	agentv1 "github.com/togettoyou/zke/api/agent/v1"
)

const (
	MaxCollectorImageLength = 512
	// Bounds on what the Server may ask the collector to be configured with.
	// They are checked on both sides: the Agent is the one that has to live
	// with the resulting Pod, so it does not take the Server's word for it.
	MinCollectorScrapeSeconds = 5
	MaxCollectorScrapeSeconds = 3600
	// A Kubernetes quantity is short by construction ("500m", "512Mi"). The
	// bound is here to keep an oversized string from reaching the Agent's
	// parser at all; the parser decides whether it is a quantity.
	MaxCollectorQuantityLength = 32
)

type MetricsCollectorHandler func(
	context.Context,
	*agentv1.MetricsCollectorRequest,
) (*agentv1.MetricsCollectorResponse, error)

// MetricsCollectorStreamHandler serves the install, uninstall and status
// requests for the in-cluster collector.
//
// The Server sends what the collector should be configured with, never the
// objects themselves: the Agent owns the shape, so an installed collector
// cannot be anything other than the one this Agent version knows how to write.
func MetricsCollectorStreamHandler(
	handler MetricsCollectorHandler,
) IncomingStreamHandler {
	return func(
		ctx context.Context,
		stream *quic.Stream,
		header *agentv1.StreamHeader,
	) error {
		if handler == nil {
			return &StreamFailure{Code: StreamErrorUnsupported, Err: ErrStreamUnsupported}
		}
		request := &agentv1.MetricsCollectorRequest{}
		if err := ReadMessage(stream, request); err != nil {
			return fmt.Errorf(
				"%w: read MetricsCollectorRequest: %w",
				ErrStreamProtocol,
				err,
			)
		}
		if err := ValidateMetricsCollectorRequest(request); err != nil {
			return err
		}
		if err := requireStreamEOF(stream); err != nil {
			return err
		}
		response, err := handler(ctx, request)
		if err != nil {
			return err
		}
		if err := validateMetricsCollectorResponse(response); err != nil {
			return err
		}
		return WriteMessage(stream, response)
	}
}

func DoMetricsCollector(
	ctx context.Context,
	connection *quic.Conn,
	header *agentv1.StreamHeader,
	request *agentv1.MetricsCollectorRequest,
) (*agentv1.MetricsCollectorResponse, error) {
	if connection == nil || validateStreamHeader(header) != nil ||
		header.GetKind() != agentv1.StreamKind_STREAM_KIND_METRICS_COLLECTOR {
		return nil, ErrStreamProtocol
	}
	if err := ValidateMetricsCollectorRequest(request); err != nil {
		return nil, err
	}
	stream, err := connection.OpenStreamSync(ctx)
	if err != nil {
		return nil, fmt.Errorf("open Agent Metrics Collector Stream: %w", err)
	}
	finished := false
	defer func() {
		if !finished {
			AbortStream(stream, StreamErrorCanceled)
		}
	}()
	stopCancellation := context.AfterFunc(ctx, func() {
		AbortStream(stream, streamErrorCode(ctx.Err(), ctx))
	})
	defer stopCancellation()
	if deadline, ok := ctx.Deadline(); ok {
		if err := stream.SetDeadline(deadline); err != nil {
			return nil, err
		}
	} else if err := stream.SetDeadline(time.Now().Add(
		time.Duration(header.GetTimeoutMillis()) * time.Millisecond,
	)); err != nil {
		return nil, err
	}
	if err := WriteMessage(stream, header); err != nil {
		return nil, err
	}
	if err := WriteMessage(stream, request); err != nil {
		return nil, err
	}
	if err := stream.Close(); err != nil {
		return nil, err
	}
	response := &agentv1.MetricsCollectorResponse{}
	if err := ReadMessage(stream, response); err != nil {
		return nil, err
	}
	if err := validateMetricsCollectorResponse(response); err != nil {
		return nil, err
	}
	finished = true
	return response, nil
}

// ValidateMetricsCollectorRequest is exported so the Agent can apply the same
// rule set before it touches Kubernetes, rather than trusting that the Stream
// layer already did.
func ValidateMetricsCollectorRequest(
	request *agentv1.MetricsCollectorRequest,
) error {
	if request == nil {
		return ErrStreamProtocol
	}
	switch request.GetAction() {
	case agentv1.MetricsCollectorAction_METRICS_COLLECTOR_ACTION_STATUS,
		agentv1.MetricsCollectorAction_METRICS_COLLECTOR_ACTION_UNINSTALL:
		// Neither reads the desired configuration, so carrying any is a sign
		// the two sides disagree about what is being asked.
		if request.GetImage() != "" || request.GetImagePullPolicy() != "" ||
			request.GetScrapeInterval() != "" || request.GetBufferSize() != "" ||
			request.GetKubeletMetricsPort() != 0 ||
			request.GetCpuRequest() != "" || request.GetMemoryRequest() != "" ||
			request.GetCpuLimit() != "" || request.GetMemoryLimit() != "" {
			return ErrStreamProtocol
		}
		return nil
	case agentv1.MetricsCollectorAction_METRICS_COLLECTOR_ACTION_INSTALL:
	default:
		return ErrStreamProtocol
	}
	image := request.GetImage()
	if image == "" || len(image) > MaxCollectorImageLength ||
		strings.TrimSpace(image) != image ||
		strings.ContainsAny(image, " \t\r\n") {
		return ErrStreamProtocol
	}
	if !validCollectorPullPolicy(request.GetImagePullPolicy()) {
		return ErrStreamProtocol
	}
	seconds, err := parseCollectorSeconds(request.GetScrapeInterval())
	if err != nil || seconds < MinCollectorScrapeSeconds ||
		seconds > MaxCollectorScrapeSeconds {
		return ErrStreamProtocol
	}
	if request.GetBufferSize() == "" || len(request.GetBufferSize()) > 32 {
		return ErrStreamProtocol
	}
	if request.GetKubeletMetricsPort() < 1 ||
		request.GetKubeletMetricsPort() > 65535 {
		return ErrStreamProtocol
	}
	// Empty is allowed and means "do not set this entry on the container". The
	// shape is checked here; whether the string is a quantity Kubernetes accepts
	// is decided by the Agent, which is the side that has to write the object.
	for _, quantity := range []string{
		request.GetCpuRequest(),
		request.GetMemoryRequest(),
		request.GetCpuLimit(),
		request.GetMemoryLimit(),
	} {
		if len(quantity) > MaxCollectorQuantityLength ||
			strings.TrimSpace(quantity) != quantity {
			return ErrStreamProtocol
		}
	}
	return nil
}

func validCollectorPullPolicy(value string) bool {
	return value == "Always" || value == "IfNotPresent" || value == "Never"
}

// parseCollectorSeconds accepts the Prometheus-style second suffix the scrape
// configuration uses, so the value can be written straight into it.
func parseCollectorSeconds(value string) (int, error) {
	trimmed, found := strings.CutSuffix(value, "s")
	if !found || trimmed == "" {
		return 0, ErrStreamProtocol
	}
	seconds := 0
	for index := range len(trimmed) {
		digit := trimmed[index]
		if digit < '0' || digit > '9' {
			return 0, ErrStreamProtocol
		}
		seconds = seconds*10 + int(digit-'0')
		if seconds > MaxCollectorScrapeSeconds {
			return 0, ErrStreamProtocol
		}
	}
	return seconds, nil
}

func validateMetricsCollectorResponse(
	response *agentv1.MetricsCollectorResponse,
) error {
	if response == nil ||
		response.GetResult() == agentv1.ResultCode_RESULT_CODE_UNSPECIFIED {
		return ErrStreamProtocol
	}
	if response.GetResult() != agentv1.ResultCode_RESULT_CODE_OK {
		if response.GetReason() == "" || response.GetMessage() == "" {
			return ErrStreamProtocol
		}
		return nil
	}
	state := response.GetState()
	if state == nil || state.GetNamespace() == "" {
		return ErrStreamProtocol
	}
	if !state.GetInstalled() &&
		(state.GetImage() != "" || state.GetDesiredReplicas() != 0 ||
			state.GetReadyReplicas() != 0) {
		return ErrStreamProtocol
	}
	if state.GetDesiredReplicas() < 0 || state.GetReadyReplicas() < 0 ||
		state.GetReadyReplicas() > state.GetDesiredReplicas() {
		return ErrStreamProtocol
	}
	return nil
}

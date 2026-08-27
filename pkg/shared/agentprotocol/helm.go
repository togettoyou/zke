package agentprotocol

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/quic-go/quic-go"
	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/helmrelease"
)

const maxHelmStringLength = 4096

type helmContextKey struct{}

// HelmIdempotencyKey returns the validated StreamHeader key for the Helm
// handler currently executing, or an empty string when the Server sent none.
//
// A release change is the one operation on this Agent where a lost response is
// expensive: the caller cannot tell "the upgrade did not happen" from "the
// upgrade happened and the answer was lost", and retrying without this would
// produce a second revision of an application that was already upgraded.
func HelmIdempotencyKey(ctx context.Context) string {
	value, _ := ctx.Value(helmContextKey{}).(string)
	return value
}

// HelmHandler runs one Helm operation inside the Cluster. It is handed the
// values document and the chart archive as readers rather than as bytes: a
// chart is the largest thing this protocol carries, and the Agent streams it
// straight into Helm's loader.
type HelmHandler func(
	ctx context.Context,
	request *agentv1.HelmRequest,
	values io.Reader,
	chart io.Reader,
) (*agentv1.HelmResponse, io.Reader, error)

// HelmStreamHandler serves the release lifecycle Stream.
//
// The wire shape is the request message, then the values document, then the
// chart archive — both sized by the message, so the Agent knows where one ends
// and the next begins without a framing layer of its own. The response is the
// response message followed by the JSON report.
func HelmStreamHandler(handler HelmHandler) IncomingStreamHandler {
	return func(
		ctx context.Context,
		stream *quic.Stream,
		header *agentv1.StreamHeader,
	) error {
		if handler == nil {
			return &StreamFailure{Code: StreamErrorUnsupported, Err: ErrStreamUnsupported}
		}
		request := &agentv1.HelmRequest{}
		if err := ReadMessage(stream, request); err != nil {
			return fmt.Errorf("%w: read HelmRequest: %w", ErrStreamProtocol, err)
		}
		if err := ValidateHelmRequest(request); err != nil {
			return err
		}
		values := &io.LimitedReader{R: stream, N: int64(request.GetValuesSize())}
		chart := &io.LimitedReader{R: stream, N: int64(request.GetChartSize())}
		handlerContext := context.WithValue(
			ctx,
			helmContextKey{},
			header.GetIdempotencyKey(),
		)
		response, report, err := handler(handlerContext, request, values, chart)
		if err != nil {
			return err
		}
		// A handler that refused before reading, or that stopped reading the
		// chart early, still leaves the Stream positioned mid-body. Drain what
		// is left in order so the EOF check below means what it says.
		for _, remaining := range []*io.LimitedReader{values, chart} {
			if remaining.N <= 0 {
				continue
			}
			if _, err := io.CopyN(io.Discard, remaining, remaining.N); err != nil {
				return fmt.Errorf(
					"%w: truncated Helm request body: %w",
					ErrStreamProtocol,
					err,
				)
			}
		}
		if err := requireStreamEOF(stream); err != nil {
			return err
		}
		if err := validateHelmResponse(response, report != nil); err != nil {
			return err
		}
		if err := WriteMessage(stream, response); err != nil {
			return err
		}
		if response.GetBodySize() > 0 {
			if _, err := io.CopyN(stream, report, int64(response.GetBodySize())); err != nil {
				return fmt.Errorf("write Helm response body: %w", err)
			}
		}
		return nil
	}
}

// DoHelm runs one Helm operation on the Cluster behind connection.
func DoHelm(
	ctx context.Context,
	connection *quic.Conn,
	header *agentv1.StreamHeader,
	request *agentv1.HelmRequest,
	values io.Reader,
	chart io.Reader,
	report io.Writer,
) (*agentv1.HelmResponse, error) {
	if connection == nil {
		return nil, errors.New("Agent Connection is required")
	}
	if err := validateStreamHeader(header); err != nil {
		return nil, err
	}
	if header.GetKind() != agentv1.StreamKind_STREAM_KIND_HELM {
		return nil, ErrStreamProtocol
	}
	if err := ValidateHelmRequest(request); err != nil {
		return nil, err
	}
	if (request.GetValuesSize() > 0 && values == nil) ||
		(request.GetChartSize() > 0 && chart == nil) || report == nil {
		return nil, errors.New("Helm request bodies are required")
	}
	stream, err := connection.OpenStreamSync(ctx)
	if err != nil {
		return nil, fmt.Errorf("open Agent Helm Stream: %w", err)
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
	for _, body := range []struct {
		size   uint64
		reader io.Reader
	}{
		{request.GetValuesSize(), values},
		{request.GetChartSize(), chart},
	} {
		if body.size == 0 {
			continue
		}
		if _, err := io.CopyN(stream, body.reader, int64(body.size)); err != nil {
			return nil, fmt.Errorf("write Helm request body: %w", err)
		}
	}
	if err := stream.Close(); err != nil {
		return nil, fmt.Errorf("finish Agent Helm request: %w", err)
	}
	response := &agentv1.HelmResponse{}
	if err := ReadMessage(stream, response); err != nil {
		return nil, err
	}
	if err := validateHelmResponse(response, true); err != nil {
		return nil, err
	}
	if response.GetBodySize() > 0 {
		if _, err := io.CopyN(report, stream, int64(response.GetBodySize())); err != nil {
			return nil, fmt.Errorf("read Helm response body: %w", err)
		}
	}
	if err := requireStreamEOF(stream); err != nil {
		return nil, err
	}
	finished = true
	return response, nil
}

// ValidateHelmRequest is exported so the Agent applies the same rules before it
// runs Helm, rather than trusting that the Stream layer already did.
//
// It checks shape, not policy: whether the Namespace is one this operator may
// write is the Server's decision and is made before the request is sent.
func ValidateHelmRequest(request *agentv1.HelmRequest) error {
	if request == nil ||
		!validResourceSegment(request.GetNamespace()) ||
		!validHelmReleaseName(request.GetReleaseName()) ||
		len(request.GetDescription()) > helmrelease.MaxDescriptionLength ||
		strings.TrimSpace(request.GetDescription()) != request.GetDescription() ||
		request.GetTimeoutSeconds() > helmrelease.MaxTimeoutSeconds ||
		request.GetMaxHistory() > helmrelease.MaxHistoryLimit ||
		request.GetRevision() < 0 {
		return ErrStreamProtocol
	}
	if request.GetValuesSize() > helmrelease.MaxValuesBytes ||
		request.GetChartSize() > helmrelease.MaxChartBytes {
		return ErrStreamBodyTooLarge
	}
	switch request.GetAction() {
	case agentv1.HelmAction_HELM_ACTION_INSTALL:
		// A chart is what an install is. Rejecting an empty one here keeps the
		// failure at the protocol edge instead of inside Helm's loader.
		if request.GetChartSize() == 0 ||
			request.GetResetValues() || request.GetReuseValues() ||
			request.GetRevision() != 0 || request.GetKeepHistory() {
			return ErrStreamProtocol
		}
	case agentv1.HelmAction_HELM_ACTION_UPGRADE:
		if request.GetChartSize() == 0 ||
			request.GetCreateNamespace() ||
			(request.GetResetValues() && request.GetReuseValues()) ||
			request.GetRevision() != 0 || request.GetKeepHistory() {
			return ErrStreamProtocol
		}
	case agentv1.HelmAction_HELM_ACTION_ROLLBACK:
		// A rollback replays a revision Helm already stored. Sending a chart or
		// values with one would mean the caller thinks it is choosing the
		// content, which it is not.
		if request.GetChartSize() != 0 || request.GetValuesSize() != 0 ||
			request.GetCreateNamespace() || request.GetResetValues() ||
			request.GetReuseValues() || request.GetKeepHistory() {
			return ErrStreamProtocol
		}
	case agentv1.HelmAction_HELM_ACTION_UNINSTALL:
		if request.GetChartSize() != 0 || request.GetValuesSize() != 0 ||
			request.GetCreateNamespace() || request.GetResetValues() ||
			request.GetReuseValues() || request.GetRevision() != 0 ||
			request.GetAtomic() || request.GetMaxHistory() != 0 {
			return ErrStreamProtocol
		}
	default:
		return ErrStreamProtocol
	}
	return nil
}

func validateHelmResponse(response *agentv1.HelmResponse, hasBody bool) error {
	if response == nil ||
		response.GetResult() == agentv1.ResultCode_RESULT_CODE_UNSPECIFIED ||
		len(response.GetReason()) > maxHelmStringLength ||
		len(response.GetMessage()) > maxHelmStringLength {
		return ErrStreamProtocol
	}
	if response.GetBodySize() > helmrelease.MaxReportBytes {
		return ErrStreamBodyTooLarge
	}
	if response.GetBodySize() > 0 && !hasBody {
		return ErrStreamProtocol
	}
	// A failure explains itself. Without this an Agent could report a refusal
	// the Server can only render as an empty box.
	if response.GetResult() != agentv1.ResultCode_RESULT_CODE_OK &&
		(response.GetReason() == "" || response.GetMessage() == "") {
		return ErrStreamProtocol
	}
	return nil
}

// validHelmReleaseName applies Helm's own rule: a release name is a DNS-1123
// subdomain, and Helm additionally caps it at 53 characters so the names it
// derives from it stay inside Kubernetes' own limits.
func validHelmReleaseName(name string) bool {
	if name == "" || len(name) > 53 {
		return false
	}
	previousDot := true
	for index := range len(name) {
		character := name[index]
		switch {
		case character >= 'a' && character <= 'z',
			character >= '0' && character <= '9':
			previousDot = false
		case character == '-':
			if index == 0 || index == len(name)-1 || previousDot {
				return false
			}
			previousDot = false
		case character == '.':
			if index == 0 || index == len(name)-1 || previousDot ||
				name[index-1] == '-' {
				return false
			}
			previousDot = true
		default:
			return false
		}
	}
	return !previousDot
}

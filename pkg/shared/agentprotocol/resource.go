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
)

const (
	DefaultMaxResourceBodySize uint64 = 32 * 1024 * 1024
	maxResourceStringLength           = 4096
)

type ResourceHandler func(
	context.Context,
	*agentv1.ResourceRequest,
	io.Reader,
) (*agentv1.ResourceResponse, io.Reader, error)

func ResourceStreamHandler(
	maxBodySize uint64,
	handler ResourceHandler,
) IncomingStreamHandler {
	if maxBodySize == 0 {
		maxBodySize = DefaultMaxResourceBodySize
	}
	return func(
		ctx context.Context,
		stream *quic.Stream,
		header *agentv1.StreamHeader,
	) error {
		if handler == nil {
			return &StreamFailure{
				Code: StreamErrorUnsupported,
				Err:  ErrStreamUnsupported,
			}
		}
		request := &agentv1.ResourceRequest{}
		if err := ReadMessage(stream, request); err != nil {
			return fmt.Errorf(
				"%w: read ResourceRequest: %w",
				ErrStreamProtocol,
				err,
			)
		}
		if err := validateResourceRequest(header, request, maxBodySize); err != nil {
			return err
		}
		requestBody := &io.LimitedReader{
			R: stream,
			N: int64(request.GetBodySize()),
		}
		response, responseBody, err := handler(ctx, request, requestBody)
		if err != nil {
			return err
		}
		if requestBody.N > 0 {
			if _, err := io.CopyN(io.Discard, requestBody, requestBody.N); err != nil {
				return fmt.Errorf(
					"%w: truncated Resource request body: %w",
					ErrStreamProtocol,
					err,
				)
			}
		}
		if err := requireStreamEOF(stream); err != nil {
			return err
		}
		if err := validateResourceResponse(
			response,
			responseBody != nil,
			maxBodySize,
		); err != nil {
			return err
		}
		if err := WriteMessage(stream, response); err != nil {
			return err
		}
		if response.GetBodySize() > 0 {
			if _, err := io.CopyN(
				stream,
				responseBody,
				int64(response.GetBodySize()),
			); err != nil {
				return fmt.Errorf("write Resource response body: %w", err)
			}
		}
		return nil
	}
}

func DoResource(
	ctx context.Context,
	connection *quic.Conn,
	header *agentv1.StreamHeader,
	request *agentv1.ResourceRequest,
	requestBody io.Reader,
	responseBody io.Writer,
	maxBodySize uint64,
) (*agentv1.ResourceResponse, error) {
	if connection == nil {
		return nil, errors.New("Agent Connection is required")
	}
	if maxBodySize == 0 {
		maxBodySize = DefaultMaxResourceBodySize
	}
	if err := validateStreamHeader(header); err != nil {
		return nil, err
	}
	if header.GetKind() != agentv1.StreamKind_STREAM_KIND_RESOURCE {
		return nil, ErrStreamProtocol
	}
	if err := validateResourceRequest(header, request, maxBodySize); err != nil {
		return nil, err
	}
	if request.GetBodySize() > 0 && requestBody == nil {
		return nil, errors.New("Resource request body is required")
	}

	stream, err := connection.OpenStreamSync(ctx)
	if err != nil {
		return nil, fmt.Errorf("open Agent Resource Stream: %w", err)
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
	} else {
		deadline := time.Now().Add(
			time.Duration(header.GetTimeoutMillis()) * time.Millisecond,
		)
		if err := stream.SetDeadline(deadline); err != nil {
			return nil, err
		}
	}
	if err := WriteMessage(stream, header); err != nil {
		return nil, err
	}
	if err := WriteMessage(stream, request); err != nil {
		return nil, err
	}
	if request.GetBodySize() > 0 {
		if _, err := io.CopyN(
			stream,
			requestBody,
			int64(request.GetBodySize()),
		); err != nil {
			return nil, fmt.Errorf("write Resource request body: %w", err)
		}
	}
	if err := stream.Close(); err != nil {
		return nil, fmt.Errorf("finish Agent Resource request: %w", err)
	}

	response := &agentv1.ResourceResponse{}
	if err := ReadMessage(stream, response); err != nil {
		return nil, err
	}
	if err := validateResourceResponse(
		response,
		responseBody != nil,
		maxBodySize,
	); err != nil {
		return nil, err
	}
	if response.GetBodySize() > 0 {
		if _, err := io.CopyN(
			responseBody,
			stream,
			int64(response.GetBodySize()),
		); err != nil {
			return nil, fmt.Errorf("read Resource response body: %w", err)
		}
	}
	if err := requireStreamEOF(stream); err != nil {
		return nil, err
	}
	finished = true
	return response, nil
}

func validateResourceRequest(
	header *agentv1.StreamHeader,
	request *agentv1.ResourceRequest,
	maxBodySize uint64,
) error {
	if request == nil ||
		request.GetVerb() == agentv1.ResourceVerb_RESOURCE_VERB_UNSPECIFIED ||
		request.GetResource() == nil ||
		!validResourceSegment(request.GetResource().GetVersion()) ||
		!validResourceSegment(request.GetResource().GetResource()) ||
		len(request.GetResource().GetGroup()) > 253 ||
		strings.TrimSpace(request.GetResource().GetGroup()) !=
			request.GetResource().GetGroup() ||
		len(request.GetNamespace()) > 253 ||
		len(request.GetName()) > 253 ||
		len(request.GetSubresource()) > 253 ||
		request.GetBodySize() > maxBodySize {
		if request != nil && request.GetBodySize() > maxBodySize {
			return ErrStreamBodyTooLarge
		}
		return ErrStreamProtocol
	}
	if request.GetListOptions() != nil {
		options := request.GetListOptions()
		if len(options.GetLabelSelector()) > maxResourceStringLength ||
			len(options.GetFieldSelector()) > maxResourceStringLength ||
			len(options.GetContinueToken()) > maxResourceStringLength ||
			len(options.GetResourceVersion()) > maxResourceStringLength {
			return ErrStreamProtocol
		}
	}
	switch request.GetVerb() {
	case agentv1.ResourceVerb_RESOURCE_VERB_LIST:
		if request.GetName() != "" ||
			request.GetBodySize() != 0 ||
			header.GetIdempotencyKey() != "" {
			return ErrStreamProtocol
		}
	case agentv1.ResourceVerb_RESOURCE_VERB_GET:
		if request.GetName() == "" ||
			request.GetBodySize() != 0 ||
			header.GetIdempotencyKey() != "" {
			return ErrStreamProtocol
		}
	case agentv1.ResourceVerb_RESOURCE_VERB_CREATE:
		if request.GetName() != "" ||
			request.GetBodySize() == 0 ||
			header.GetIdempotencyKey() == "" {
			return ErrStreamProtocol
		}
	case agentv1.ResourceVerb_RESOURCE_VERB_UPDATE,
		agentv1.ResourceVerb_RESOURCE_VERB_PATCH:
		if request.GetName() == "" ||
			request.GetBodySize() == 0 ||
			header.GetIdempotencyKey() == "" {
			return ErrStreamProtocol
		}
	case agentv1.ResourceVerb_RESOURCE_VERB_DELETE:
		if request.GetName() == "" ||
			request.GetBodySize() != 0 ||
			header.GetIdempotencyKey() == "" {
			return ErrStreamProtocol
		}
	default:
		return ErrStreamProtocol
	}
	return nil
}

func validateResourceResponse(
	response *agentv1.ResourceResponse,
	hasBody bool,
	maxBodySize uint64,
) error {
	if response == nil ||
		response.GetResult() == agentv1.ResultCode_RESULT_CODE_UNSPECIFIED ||
		len(response.GetReason()) > maxResourceStringLength ||
		len(response.GetMessage()) > maxResourceStringLength ||
		len(response.GetContentType()) > 256 ||
		response.GetBodySize() > maxBodySize {
		if response != nil && response.GetBodySize() > maxBodySize {
			return ErrStreamBodyTooLarge
		}
		return ErrStreamProtocol
	}
	if response.GetBodySize() > 0 && !hasBody {
		return ErrStreamProtocol
	}
	return nil
}

func validResourceSegment(value string) bool {
	return value != "" &&
		len(value) <= 253 &&
		strings.TrimSpace(value) == value &&
		!strings.ContainsAny(value, "/?#")
}

func requireStreamEOF(reader io.Reader) error {
	var trailing [1]byte
	if _, err := reader.Read(trailing[:]); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("%w: trailing Resource Stream data", ErrStreamProtocol)
		}
		return fmt.Errorf(
			"%w: finish Resource Stream: %w",
			ErrStreamProtocol,
			err,
		)
	}
	return nil
}

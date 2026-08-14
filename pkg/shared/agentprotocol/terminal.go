package agentprotocol

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/quic-go/quic-go"
	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/validation"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
)

const (
	MinTerminalSessionTTLSeconds uint64 = 60
	MaxTerminalSessionTTLSeconds uint64 = 60 * 60
	MaxTerminalPermissions              = 64
)

type TerminalSessionHandler func(context.Context, *agentv1.TerminalSessionRequest) (*agentv1.TerminalSessionResponse, error)

func TerminalSessionStreamHandler(handler TerminalSessionHandler) IncomingStreamHandler {
	return func(ctx context.Context, stream *quic.Stream, header *agentv1.StreamHeader) error {
		if handler == nil {
			return &StreamFailure{Code: StreamErrorUnsupported, Err: ErrStreamUnsupported}
		}
		request := &agentv1.TerminalSessionRequest{}
		if err := ReadMessage(stream, request); err != nil {
			return fmt.Errorf("%w: read TerminalSessionRequest: %w", ErrStreamProtocol, err)
		}
		if validateTerminalSessionRequest(header, request) != nil {
			return ErrStreamProtocol
		}
		if err := requireStreamEOF(stream); err != nil {
			return err
		}
		response, err := handler(ctx, request)
		if err != nil {
			return err
		}
		if validateTerminalSessionResponse(response, request) != nil {
			return ErrStreamProtocol
		}
		return WriteMessage(stream, response)
	}
}

func DoTerminalSession(
	ctx context.Context,
	connection *quic.Conn,
	header *agentv1.StreamHeader,
	request *agentv1.TerminalSessionRequest,
) (*agentv1.TerminalSessionResponse, error) {
	if connection == nil || validateStreamHeader(header) != nil ||
		header.GetKind() != agentv1.StreamKind_STREAM_KIND_TERMINAL_SESSION ||
		validateTerminalSessionRequest(header, request) != nil {
		return nil, ErrStreamProtocol
	}
	stream, err := connection.OpenStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	if deadline, ok := ctx.Deadline(); ok {
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
	if err := stream.Close(); err != nil {
		return nil, err
	}
	response := &agentv1.TerminalSessionResponse{}
	if err := ReadMessage(stream, response); err != nil {
		return nil, err
	}
	if validateTerminalSessionResponse(response, request) != nil {
		return nil, ErrStreamProtocol
	}
	return response, nil
}

func validateTerminalSessionRequest(header *agentv1.StreamHeader, request *agentv1.TerminalSessionRequest) error {
	if header == nil || request == nil || !validation.IsUUID(request.GetSessionId()) ||
		!validation.IsUUID(request.GetUserId()) ||
		len(k8svalidation.IsDNS1123Label(request.GetNamespace())) != 0 ||
		!validation.IsIdempotencyKey(header.GetIdempotencyKey()) {
		return ErrStreamProtocol
	}
	switch request.GetAction() {
	case agentv1.TerminalSessionAction_TERMINAL_SESSION_ACTION_CREATE:
		if request.GetTtlSeconds() < MinTerminalSessionTTLSeconds ||
			request.GetTtlSeconds() > MaxTerminalSessionTTLSeconds ||
			len(request.GetPermissions()) == 0 || len(request.GetPermissions()) > MaxTerminalPermissions ||
			len(request.GetImage()) == 0 || len(request.GetImage()) > 512 ||
			request.GetImage() != strings.TrimSpace(request.GetImage()) || strings.ContainsAny(request.GetImage(), " \t\r\n") ||
			!validTerminalImagePullPolicy(request.GetImagePullPolicy()) {
			return ErrStreamProtocol
		}
		permissions := append([]string(nil), request.GetPermissions()...)
		slices.Sort(permissions)
		if slices.Contains(permissions, "") {
			return ErrStreamProtocol
		}
		for index := 1; index < len(permissions); index++ {
			if permissions[index] == permissions[index-1] {
				return ErrStreamProtocol
			}
		}
	case agentv1.TerminalSessionAction_TERMINAL_SESSION_ACTION_DELETE:
		if request.GetTtlSeconds() != 0 || len(request.GetPermissions()) != 0 || request.GetImage() != "" ||
			request.GetImagePullPolicy() != "" {
			return ErrStreamProtocol
		}
	default:
		return ErrStreamProtocol
	}
	return nil
}

func validTerminalImagePullPolicy(value string) bool {
	return value == "Always" || value == "IfNotPresent" || value == "Never"
}

func validateTerminalSessionResponse(response *agentv1.TerminalSessionResponse, request *agentv1.TerminalSessionRequest) error {
	if response == nil || request == nil || response.GetResult() == agentv1.ResultCode_RESULT_CODE_UNSPECIFIED ||
		response.GetSessionId() != request.GetSessionId() || response.GetNamespace() != request.GetNamespace() {
		return ErrStreamProtocol
	}
	if response.GetResult() != agentv1.ResultCode_RESULT_CODE_OK {
		if response.GetReason() == "" {
			return ErrStreamProtocol
		}
		return nil
	}
	if request.GetAction() == agentv1.TerminalSessionAction_TERMINAL_SESSION_ACTION_DELETE {
		return nil
	}
	if len(k8svalidation.IsDNS1123Subdomain(response.GetPodName())) != 0 || response.GetPodUid() == "" ||
		len(k8svalidation.IsDNS1123Label(response.GetContainer())) != 0 || response.GetExpiresAtUnix() <= 0 {
		return errors.New("invalid successful TerminalSessionResponse")
	}
	return nil
}

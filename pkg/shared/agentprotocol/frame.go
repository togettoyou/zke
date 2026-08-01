package agentprotocol

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"reflect"

	"github.com/quic-go/quic-go"
	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"google.golang.org/protobuf/proto"
)

const (
	ProtocolVersion               uint32 = 1
	ProtocolVersionLabel                 = "v1"
	ALPN                                 = "zke-agent/1"
	MaxFrameSize                         = 64 * 1024
	CapabilityCertificateRenewal         = "certificate-renewal-v1"
	CapabilityResourceV1                 = "resource.v1"
	CapabilityResourceDiscoveryV1        = "resource-discovery.v1"
	CapabilityResourceWriteV1            = "resource-write.v1"
	CapabilityPodLogsV1                  = "pod-logs.v1"

	CloseNormal              quic.ApplicationErrorCode = 0
	CloseProtocolError       quic.ApplicationErrorCode = 1
	CloseAuthenticationError quic.ApplicationErrorCode = 2
	CloseHeartbeatTimeout    quic.ApplicationErrorCode = 3
	CloseConnectionReplaced  quic.ApplicationErrorCode = 4
	CloseInternalError       quic.ApplicationErrorCode = 5
	CloseScopeSuspended      quic.ApplicationErrorCode = 6
)

// GoAway reasons are part of the Server–Agent contract: the Agent decides
// whether to reconnect from the reason alone, so both sides must use these
// constants instead of ad-hoc strings.
const (
	GoAwayServerShutdown     = "server_shutdown"
	GoAwayConnectionReplaced = "connection_replaced"
	GoAwayCredentialRevoked  = "credential_revoked"
	GoAwayAgentRevoked       = "agent_revoked"
	GoAwayClusterRevoked     = "cluster_revoked"
	GoAwayScopeSuspended     = "scope_suspended"
)

// GoAwayIsPermanent reports whether a GoAway reason means the Agent must stop
// reconnecting with its current identity. Unknown reasons are treated as
// transient so that a newer Server cannot accidentally strand an older Agent.
func GoAwayIsPermanent(reason string) bool {
	switch reason {
	case GoAwayCredentialRevoked, GoAwayAgentRevoked, GoAwayClusterRevoked:
		return true
	default:
		return false
	}
}

var (
	ErrFrameTooLarge = errors.New("Agent protocol frame exceeds maximum size")
	ErrEmptyFrame    = errors.New("Agent protocol frame is empty")
)

func WriteMessage(writer io.Writer, message proto.Message) error {
	if message == nil {
		return ErrEmptyFrame
	}
	value := reflect.ValueOf(message)
	if value.Kind() == reflect.Pointer && value.IsNil() {
		return ErrEmptyFrame
	}
	payload, err := proto.Marshal(message)
	if err != nil {
		return fmt.Errorf("marshal Agent protocol message: %w", err)
	}
	if len(payload) == 0 {
		return ErrEmptyFrame
	}
	if len(payload) > MaxFrameSize {
		return ErrFrameTooLarge
	}

	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], uint32(len(payload)))
	if err := writeAll(writer, prefix[:]); err != nil {
		return fmt.Errorf("write Agent protocol message length: %w", err)
	}
	if err := writeAll(writer, payload); err != nil {
		return fmt.Errorf("write Agent protocol message: %w", err)
	}
	return nil
}

func ReadMessage(reader io.Reader, message proto.Message) error {
	if message == nil {
		return ErrEmptyFrame
	}
	value := reflect.ValueOf(message)
	if value.Kind() != reflect.Pointer || value.IsNil() {
		return ErrEmptyFrame
	}
	var prefix [4]byte
	if _, err := io.ReadFull(reader, prefix[:]); err != nil {
		return fmt.Errorf("read Agent protocol message length: %w", err)
	}
	size := binary.BigEndian.Uint32(prefix[:])
	if size == 0 {
		return ErrEmptyFrame
	}
	if size > MaxFrameSize {
		return ErrFrameTooLarge
	}

	payload := make([]byte, size)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return fmt.Errorf("read Agent protocol message: %w", err)
	}
	if err := proto.Unmarshal(payload, message); err != nil {
		return fmt.Errorf("unmarshal Agent protocol message: %w", err)
	}
	return nil
}

func WriteFrame(writer io.Writer, frame *agentv1.ControlFrame) error {
	if frame == nil || frame.Message == nil {
		return ErrEmptyFrame
	}
	return WriteMessage(writer, frame)
}

func ReadFrame(reader io.Reader) (*agentv1.ControlFrame, error) {
	frame := &agentv1.ControlFrame{}
	if err := ReadMessage(reader, frame); err != nil {
		return nil, err
	}
	if frame.Message == nil {
		return nil, ErrEmptyFrame
	}
	return frame, nil
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}

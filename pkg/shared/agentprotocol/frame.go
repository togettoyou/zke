package agentprotocol

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/quic-go/quic-go"
	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"google.golang.org/protobuf/proto"
)

const (
	ProtocolVersion              uint32 = 1
	ProtocolVersionLabel                = "v1"
	ALPN                                = "zke-agent/1"
	MaxFrameSize                        = 64 * 1024
	CapabilityCertificateRenewal        = "certificate-renewal-v1"

	CloseNormal              quic.ApplicationErrorCode = 0
	CloseProtocolError       quic.ApplicationErrorCode = 1
	CloseAuthenticationError quic.ApplicationErrorCode = 2
	CloseHeartbeatTimeout    quic.ApplicationErrorCode = 3
	CloseConnectionReplaced  quic.ApplicationErrorCode = 4
	CloseInternalError       quic.ApplicationErrorCode = 5
)

var (
	ErrFrameTooLarge = errors.New("Agent protocol frame exceeds maximum size")
	ErrEmptyFrame    = errors.New("Agent protocol frame is empty")
)

func WriteFrame(writer io.Writer, frame *agentv1.ControlFrame) error {
	if frame == nil || frame.Message == nil {
		return ErrEmptyFrame
	}
	payload, err := proto.Marshal(frame)
	if err != nil {
		return fmt.Errorf("marshal Agent protocol frame: %w", err)
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
		return fmt.Errorf("write Agent protocol frame length: %w", err)
	}
	if err := writeAll(writer, payload); err != nil {
		return fmt.Errorf("write Agent protocol frame: %w", err)
	}
	return nil
}

func ReadFrame(reader io.Reader) (*agentv1.ControlFrame, error) {
	var prefix [4]byte
	if _, err := io.ReadFull(reader, prefix[:]); err != nil {
		return nil, fmt.Errorf("read Agent protocol frame length: %w", err)
	}
	size := binary.BigEndian.Uint32(prefix[:])
	if size == 0 {
		return nil, ErrEmptyFrame
	}
	if size > MaxFrameSize {
		return nil, ErrFrameTooLarge
	}

	payload := make([]byte, size)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, fmt.Errorf("read Agent protocol frame: %w", err)
	}
	frame := &agentv1.ControlFrame{}
	if err := proto.Unmarshal(payload, frame); err != nil {
		return nil, fmt.Errorf("unmarshal Agent protocol frame: %w", err)
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

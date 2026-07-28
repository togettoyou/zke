package agentprotocol

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
)

func TestControlFrameRoundTrip(t *testing.T) {
	t.Parallel()

	input := &agentv1.ControlFrame{
		ProtocolVersion: ProtocolVersion,
		Message: &agentv1.ControlFrame_ClientHello{
			ClientHello: &agentv1.ClientHello{
				AgentId:      "00000000-0000-4000-8000-000000000004",
				ClusterId:    "00000000-0000-4000-8000-000000000003",
				AgentVersion: "development",
				StartupId:    "00000000-0000-4000-8000-000000000005",
			},
		},
	}

	var buffer bytes.Buffer
	if err := WriteFrame(&buffer, input); err != nil {
		t.Fatal(err)
	}
	output, err := ReadFrame(&buffer)
	if err != nil {
		t.Fatal(err)
	}
	hello := output.GetClientHello()
	if output.GetProtocolVersion() != ProtocolVersion ||
		hello.GetAgentId() != input.GetClientHello().GetAgentId() ||
		hello.GetClusterId() != input.GetClientHello().GetClusterId() ||
		hello.GetStartupId() != input.GetClientHello().GetStartupId() {
		t.Fatalf("unexpected round-trip frame: %+v", output)
	}
}

func TestReadFrameRejectsOversizedPayloadBeforeAllocation(t *testing.T) {
	t.Parallel()

	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], MaxFrameSize+1)
	_, err := ReadFrame(bytes.NewReader(prefix[:]))
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("ReadFrame() error = %v, want ErrFrameTooLarge", err)
	}
}

func TestWriteFrameRejectsMissingMessage(t *testing.T) {
	t.Parallel()

	err := WriteFrame(&bytes.Buffer{}, &agentv1.ControlFrame{
		ProtocolVersion: ProtocolVersion,
	})
	if !errors.Is(err, ErrEmptyFrame) {
		t.Fatalf("WriteFrame() error = %v, want ErrEmptyFrame", err)
	}
}

func TestScopeSuspensionIsRetryable(t *testing.T) {
	t.Parallel()

	if GoAwayIsPermanent(GoAwayScopeSuspended) {
		t.Fatal("scope suspension must not permanently strand the Agent")
	}
}

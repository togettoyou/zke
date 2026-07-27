package agent

import (
	"errors"
	"io"
	"net"
	"testing"
	"time"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
)

// controlFrameDelay bounds how long a dispatched frame may take to reach the
// connection loop. It is far below any heartbeat interval on purpose: the point
// of reading the Control Stream continuously is that a Server-initiated frame
// no longer waits for the next tick.
const controlFrameDelay = 2 * time.Second

func controlReaderPipe(t *testing.T) (*controlReader, net.Conn) {
	t.Helper()

	server, client := net.Pipe()
	reader := startControlReader(client)
	t.Cleanup(func() {
		reader.stop()
		_ = server.Close()
		_ = client.Close()
	})
	return reader, server
}

// writeControlFrame reports a write failure with Errorf rather than Fatalf:
// these writes run on their own goroutine so the reader has something to
// consume, and Fatalf outside the test goroutine would not fail the test.
func writeControlFrame(
	t *testing.T,
	writer io.Writer,
	message *agentv1.ControlFrame,
) {
	t.Helper()

	if err := agentprotocol.WriteFrame(writer, message); err != nil {
		t.Errorf("write control frame: %v", err)
	}
}

// A GoAway must surface as soon as the Server sends it. Under the previous
// lock-step loop it was only noticed after the next heartbeat, so a revoked
// Agent kept running for up to a full heartbeat interval.
func TestControlReaderDeliversGoAwayWithoutAHeartbeat(t *testing.T) {
	t.Parallel()

	reader, server := controlReaderPipe(t)
	go writeControlFrame(t, server, &agentv1.ControlFrame{
		ProtocolVersion: agentprotocol.ProtocolVersion,
		Message: &agentv1.ControlFrame_GoAway{
			GoAway: &agentv1.GoAway{
				Reason: agentprotocol.GoAwayAgentRevoked,
			},
		},
	})

	select {
	case goAway := <-reader.goAways:
		if goAway.GetReason() != agentprotocol.GoAwayAgentRevoked {
			t.Fatalf("GoAway reason = %q", goAway.GetReason())
		}
	case err := <-reader.failures:
		t.Fatalf("reader failed instead of reporting GoAway: %v", err)
	case <-time.After(controlFrameDelay):
		t.Fatal("GoAway was not delivered without a heartbeat in flight")
	}
}

// Each frame type must reach the waiter that asked for it. A renewal response
// arriving while a heartbeat is outstanding used to be read as a malformed
// acknowledgement.
func TestControlReaderDispatchesByFrameType(t *testing.T) {
	t.Parallel()

	reader, server := controlReaderPipe(t)
	go func() {
		writeControlFrame(t, server, &agentv1.ControlFrame{
			ProtocolVersion: agentprotocol.ProtocolVersion,
			Message: &agentv1.ControlFrame_HeartbeatAck{
				HeartbeatAck: &agentv1.HeartbeatAck{Sequence: 7},
			},
		})
		writeControlFrame(t, server, &agentv1.ControlFrame{
			ProtocolVersion: agentprotocol.ProtocolVersion,
			Message: &agentv1.ControlFrame_CertificateRenewalResponse{
				CertificateRenewalResponse: &agentv1.CertificateRenewalResponse{
					CertificatePem:                "renewed",
					CertificateExpiresAtUnixMilli: 1,
				},
			},
		})
	}()

	select {
	case acknowledged := <-reader.heartbeatAcks:
		if acknowledged.GetSequence() != 7 {
			t.Fatalf("acknowledged sequence = %d, want 7", acknowledged.GetSequence())
		}
	case <-time.After(controlFrameDelay):
		t.Fatal("heartbeat acknowledgement was not delivered")
	}
	select {
	case renewal := <-reader.renewals:
		if renewal.GetCertificatePem() != "renewed" {
			t.Fatalf("renewal certificate = %q", renewal.GetCertificatePem())
		}
	case <-time.After(controlFrameDelay):
		t.Fatal("certificate renewal response was not delivered")
	}
}

func TestControlReaderReportsProtocolFailures(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name  string
		frame *agentv1.ControlFrame
		want  error
	}{
		{
			name: "unsupported protocol version",
			frame: &agentv1.ControlFrame{
				ProtocolVersion: agentprotocol.ProtocolVersion + 1,
				Message: &agentv1.ControlFrame_HeartbeatAck{
					HeartbeatAck: &agentv1.HeartbeatAck{Sequence: 1},
				},
			},
			want: ErrControlProtocolVersion,
		},
		{
			// ClientHello only ever travels Agent to Server; receiving one back
			// means the peer is not speaking this protocol.
			name: "frame the Agent never expects to receive",
			frame: &agentv1.ControlFrame{
				ProtocolVersion: agentprotocol.ProtocolVersion,
				Message: &agentv1.ControlFrame_ClientHello{
					ClientHello: &agentv1.ClientHello{AgentId: "unexpected"},
				},
			},
			want: ErrControlFrameUnexpected,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			reader, server := controlReaderPipe(t)
			go writeControlFrame(t, server, testCase.frame)

			select {
			case err := <-reader.failures:
				if !errors.Is(err, testCase.want) {
					t.Fatalf("failure = %v, want %v", err, testCase.want)
				}
				// These are protocol violations: reconnecting cannot fix them.
				if !permanentAgentConnectionError(err) {
					t.Fatalf("%v is retried instead of stopping the Agent", err)
				}
			case <-time.After(controlFrameDelay):
				t.Fatal("protocol failure was not reported")
			}
		})
	}
}

// The reading goroutine must not outlive the connection loop, including when
// it is parked handing over a frame that nobody will ever receive.
func TestControlReaderGoroutineEndsWhileDeliveringAFrame(t *testing.T) {
	t.Parallel()

	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	reader := startControlReader(client)

	go writeControlFrame(t, server, &agentv1.ControlFrame{
		ProtocolVersion: agentprotocol.ProtocolVersion,
		Message: &agentv1.ControlFrame_HeartbeatAck{
			HeartbeatAck: &agentv1.HeartbeatAck{Sequence: 1},
		},
	})

	select {
	case <-reader.heartbeatAcks:
	case <-time.After(controlFrameDelay):
		t.Fatal("first acknowledgement was not delivered")
	}

	// net.Pipe is unbuffered, so this write returns only once the reader has
	// consumed the whole frame. Nothing receives this one, which leaves the
	// reader parked on the handover — the case where an unconditional send
	// would strand the goroutine.
	writeControlFrame(t, server, &agentv1.ControlFrame{
		ProtocolVersion: agentprotocol.ProtocolVersion,
		Message: &agentv1.ControlFrame_HeartbeatAck{
			HeartbeatAck: &agentv1.HeartbeatAck{Sequence: 2},
		},
	})

	reader.stop()
	select {
	case <-reader.finished:
	case <-time.After(controlFrameDelay):
		t.Fatal("reading goroutine stayed parked on an undelivered frame")
	}
}

// A transport read failure must reach the connection loop, which turns it into
// a reconnect, and the goroutine must then end. Reporting it is why the reader
// does not simply return on error.
func TestControlReaderReportsAClosedStreamAndEnds(t *testing.T) {
	t.Parallel()

	server, client := net.Pipe()
	defer server.Close()
	reader := startControlReader(client)
	defer reader.stop()

	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-reader.failures:
		if err == nil {
			t.Fatal("closed stream reported a nil failure")
		}
		// A dead transport is retried, not treated as a protocol violation.
		if permanentAgentConnectionError(err) {
			t.Fatalf("closed stream stops the Agent instead of reconnecting: %v", err)
		}
	case <-time.After(controlFrameDelay):
		t.Fatal("closing the stream was not reported to the connection loop")
	}
	select {
	case <-reader.finished:
	case <-time.After(controlFrameDelay):
		t.Fatal("reading goroutine outlived the stream it reads")
	}
}

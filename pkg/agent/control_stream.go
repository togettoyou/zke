package agent

import (
	"io"
	"time"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
)

// controlReader owns every read on the Control Stream and dispatches frames by
// type.
//
// Reading continuously rather than only after sending a heartbeat is what lets
// the Server push a message at any time: a GoAway is acted on when it arrives
// instead of waiting for the next heartbeat tick, and a frame that is not a
// heartbeat acknowledgement no longer looks like a malformed one.
//
// Only this goroutine reads the stream. Writes stay on the connection loop, so
// the two never contend and the Control Stream keeps one serial writer.
type controlReader struct {
	heartbeatAcks chan *agentv1.HeartbeatAck
	renewals      chan *agentv1.CertificateRenewalResponse
	goAways       chan *agentv1.GoAway
	failures      chan error
	done          chan struct{}
	// finished closes when the reading goroutine has returned, making its
	// lifetime observable rather than assumed.
	finished chan struct{}
}

// startControlReader takes an io.Reader rather than the QUIC stream itself:
// dispatching frames needs nothing else, and the narrower dependency keeps the
// dispatch rules testable without a live connection.
func startControlReader(stream io.Reader) *controlReader {
	reader := &controlReader{
		heartbeatAcks: make(chan *agentv1.HeartbeatAck),
		renewals:      make(chan *agentv1.CertificateRenewalResponse),
		goAways:       make(chan *agentv1.GoAway),
		failures:      make(chan error),
		done:          make(chan struct{}),
		finished:      make(chan struct{}),
	}
	go reader.run(stream)
	return reader
}

// stop releases the reader without waiting for it. A goroutine parked on a
// frame handover returns immediately, but one parked on a read cannot: only
// closing the QUIC connection unblocks that, which the connection loop does on
// its way out. Callers that need to observe the goroutine actually ending wait
// on finished after the connection is closed.
func (reader *controlReader) stop() {
	close(reader.done)
}

func (reader *controlReader) run(stream io.Reader) {
	defer close(reader.finished)
	for {
		frame, err := agentprotocol.ReadFrame(stream)
		if err != nil {
			deliver(reader.failures, err, reader.done)
			return
		}
		if frame.GetProtocolVersion() != agentprotocol.ProtocolVersion {
			deliver(reader.failures, ErrControlProtocolVersion, reader.done)
			return
		}
		switch {
		case frame.GetGoAway() != nil:
			// A GoAway ends the connection, so this is the last frame worth
			// reading.
			deliver(reader.goAways, frame.GetGoAway(), reader.done)
			return
		case frame.GetHeartbeatAck() != nil:
			if !deliver(reader.heartbeatAcks, frame.GetHeartbeatAck(), reader.done) {
				return
			}
		case frame.GetCertificateRenewalResponse() != nil:
			if !deliver(
				reader.renewals,
				frame.GetCertificateRenewalResponse(),
				reader.done,
			) {
				return
			}
		default:
			deliver(reader.failures, ErrControlFrameUnexpected, reader.done)
			return
		}
	}
}

// deliver hands a frame to the connection loop, reporting whether it was
// received. A send never blocks past the reader's lifetime, so the goroutine
// cannot outlive a connection whose loop has already returned.
func deliver[T any](channel chan T, value T, done <-chan struct{}) bool {
	select {
	case channel <- value:
		return true
	case <-done:
		return false
	}
}

func stopTimer(timer *time.Timer) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}

func resetTimer(timer *time.Timer, duration time.Duration) {
	stopTimer(timer)
	timer.Reset(duration)
}

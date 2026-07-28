package agent

import (
	"crypto/x509"
	"errors"
	"fmt"
	"testing"

	"github.com/quic-go/quic-go"
	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
)

func TestPermanentAgentConnectionError(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name     string
		err      error
		expected bool
	}{
		{name: "nil", err: nil, expected: false},
		{
			name:     "wrapped ServerHello rejection",
			err:      fmt.Errorf("handshake: %w", ErrServerHelloInvalid),
			expected: true,
		},
		{
			name:     "heartbeat configuration rejection",
			err:      ErrHeartbeatConfigInvalid,
			expected: true,
		},
		{
			name:     "capability rejection",
			err:      fmt.Errorf("%w: capability count exceeds the limit", ErrServerCapability),
			expected: true,
		},
		{
			name:     "heartbeat acknowledgement rejection",
			err:      ErrHeartbeatAckInvalid,
			expected: true,
		},
		{
			name:     "GoAway credential revoked",
			err:      &GoAwayError{Reason: agentprotocol.GoAwayCredentialRevoked},
			expected: true,
		},
		{
			name:     "GoAway agent revoked",
			err:      &GoAwayError{Reason: agentprotocol.GoAwayAgentRevoked},
			expected: true,
		},
		{
			name:     "GoAway cluster revoked",
			err:      &GoAwayError{Reason: agentprotocol.GoAwayClusterRevoked},
			expected: true,
		},
		{
			name:     "GoAway server shutdown is retryable",
			err:      &GoAwayError{Reason: agentprotocol.GoAwayServerShutdown},
			expected: false,
		},
		{
			name:     "GoAway connection replaced is retryable",
			err:      &GoAwayError{Reason: agentprotocol.GoAwayConnectionReplaced},
			expected: false,
		},
		{
			name:     "GoAway scope suspended is retryable",
			err:      &GoAwayError{Reason: agentprotocol.GoAwayScopeSuspended},
			expected: false,
		},
		{
			name:     "unknown GoAway reason is retryable",
			err:      &GoAwayError{Reason: "reason_from_a_newer_server"},
			expected: false,
		},
		{
			name:     "unknown certificate authority",
			err:      x509.UnknownAuthorityError{},
			expected: true,
		},
		{
			name: "remote authentication close",
			err: &quic.ApplicationError{
				Remote:    true,
				ErrorCode: agentprotocol.CloseAuthenticationError,
			},
			expected: true,
		},
		{
			name: "local authentication close is retryable",
			err: &quic.ApplicationError{
				Remote:    false,
				ErrorCode: agentprotocol.CloseAuthenticationError,
			},
			expected: false,
		},
		{
			name: "remote heartbeat timeout is retryable",
			err: &quic.ApplicationError{
				Remote:    true,
				ErrorCode: agentprotocol.CloseHeartbeatTimeout,
			},
			expected: false,
		},
		{
			name: "remote scope suspension close is retryable",
			err: &quic.ApplicationError{
				Remote:    true,
				ErrorCode: agentprotocol.CloseScopeSuspended,
			},
			expected: false,
		},
		{
			name:     "transport failure is retryable",
			err:      errors.New("connect to Agent Listener: timeout"),
			expected: false,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := permanentAgentConnectionError(testCase.err); got != testCase.expected {
				t.Fatalf(
					"permanentAgentConnectionError(%v) = %t, want %t",
					testCase.err,
					got,
					testCase.expected,
				)
			}
		})
	}
}

// A reworded error message must not change the reconnect decision, which is
// exactly what the previous strings.Contains implementation could not promise.
func TestPermanentAgentConnectionErrorIgnoresMessageText(t *testing.T) {
	t.Parallel()

	if permanentAgentConnectionError(errors.New("ServerHello is invalid")) {
		t.Fatal("a plain error whose text mimics a sentinel must stay retryable")
	}
	if !permanentAgentConnectionError(
		fmt.Errorf("control handshake failed: %w", ErrServerHelloInvalid),
	) {
		t.Fatal("a wrapped sentinel must stay permanent regardless of surrounding text")
	}
}

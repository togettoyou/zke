package agent

import (
	"crypto/x509"
	"errors"

	"github.com/quic-go/quic-go"
	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
)

// Protocol failures that must stop the reconnect loop instead of being retried.
// They are sentinels rather than ad-hoc strings because the reconnect decision
// depends on them: matching on error text would make a message reword silently
// change whether an Agent gives up or spins forever.
var (
	ErrServerHelloInvalid     = errors.New("ServerHello is invalid")
	ErrHeartbeatConfigInvalid = errors.New("Server heartbeat configuration is invalid")
	ErrServerCapability       = errors.New("Server capability is invalid")
	ErrHeartbeatAckInvalid    = errors.New("Agent heartbeat acknowledgement is invalid")
	ErrCertificateExpired     = errors.New("Agent certificate expired")
	ErrControlFrameUnexpected = errors.New("Server sent an unexpected Control Stream frame")
	ErrControlProtocolVersion = errors.New("Server Control Stream protocol version is unsupported")
)

// ErrHeartbeatAckTimeout reports that the Server stopped acknowledging
// heartbeats. Unlike the protocol failures above it is not permanent: an
// unresponsive Server is exactly the case reconnecting is meant to recover
// from.
var ErrHeartbeatAckTimeout = errors.New("Server did not acknowledge an Agent heartbeat in time")

// GoAwayError reports that the Server asked this Agent to disconnect.
type GoAwayError struct {
	Reason string
}

func (err *GoAwayError) Error() string {
	return "Server requested Agent disconnect: " + err.Reason
}

// Permanent reports whether the Agent must stop reconnecting with its current
// identity, which is the case once its credential or Agent identity has been
// revoked. Scope suspension is deliberately retryable.
func (err *GoAwayError) Permanent() bool {
	return agentprotocol.GoAwayIsPermanent(err.Reason)
}

// permanentAgentConnectionError reports whether reconnecting with the current
// identity can never succeed, so the Agent should exit instead of retrying.
func permanentAgentConnectionError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrServerHelloInvalid) ||
		errors.Is(err, ErrHeartbeatConfigInvalid) ||
		errors.Is(err, ErrServerCapability) ||
		errors.Is(err, ErrHeartbeatAckInvalid) ||
		errors.Is(err, ErrCertificateExpired) ||
		errors.Is(err, ErrControlFrameUnexpected) ||
		errors.Is(err, ErrControlProtocolVersion) {
		return true
	}
	var goAway *GoAwayError
	if errors.As(err, &goAway) {
		return goAway.Permanent()
	}
	var unknownAuthority x509.UnknownAuthorityError
	var hostnameError x509.HostnameError
	var invalidCertificate x509.CertificateInvalidError
	if errors.As(err, &unknownAuthority) ||
		errors.As(err, &hostnameError) ||
		errors.As(err, &invalidCertificate) {
		return true
	}
	var applicationError *quic.ApplicationError
	if errors.As(err, &applicationError) {
		return applicationError.Remote &&
			(applicationError.ErrorCode == agentprotocol.CloseProtocolError ||
				applicationError.ErrorCode == agentprotocol.CloseAuthenticationError)
	}
	return false
}

package agentconn

import (
	"crypto/tls"
	"errors"
	"fmt"
	"sync/atomic"
)

// TLSCertificateReloader keeps the Agent Listener leaf certificate in memory
// and swaps it atomically after managed PKI reconciliation. Existing QUIC
// sessions keep their negotiated TLS state; new handshakes use the latest
// certificate without restarting the listener.
type TLSCertificateReloader struct {
	certificateFile string
	privateKeyFile  string
	current         atomic.Pointer[tls.Certificate]
}

func NewTLSCertificateReloader(certificateFile, privateKeyFile string) (*TLSCertificateReloader, error) {
	reloader := &TLSCertificateReloader{
		certificateFile: certificateFile,
		privateKeyFile:  privateKeyFile,
	}
	if err := reloader.Reload(); err != nil {
		return nil, err
	}
	return reloader, nil
}

func (reloader *TLSCertificateReloader) Reload() error {
	if reloader == nil {
		return errors.New("Agent Listener TLS certificate reloader is required")
	}
	certificate, err := tls.LoadX509KeyPair(reloader.certificateFile, reloader.privateKeyFile)
	if err != nil {
		return fmt.Errorf("load Agent Listener TLS certificate: %w", err)
	}
	reloader.current.Store(&certificate)
	return nil
}

func (reloader *TLSCertificateReloader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	if reloader == nil {
		return nil, errors.New("Agent Listener TLS certificate reloader is required")
	}
	certificate := reloader.current.Load()
	if certificate == nil {
		return nil, errors.New("Agent Listener TLS certificate is unavailable")
	}
	return certificate, nil
}

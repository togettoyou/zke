package agentprotocol

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"errors"
	"io"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	agentv1 "github.com/togettoyou/zke/api/agent/v1"
)

func TestRealQUICStreamFailuresAreIsolated(t *testing.T) {
	client, server, stop := openStreamTestConnection(t)
	defer stop()
	streamServer, err := NewStreamServer(StreamServerConfig{
		HeaderTimeout: 200 * time.Millisecond,
		MaxTimeout:    2 * time.Second,
		Handlers: map[agentv1.StreamKind]StreamHandlerConfig{
			agentv1.StreamKind_STREAM_KIND_RESOURCE: {
				MaxConcurrent: 4,
				Handle: ResourceStreamHandler(
					1024,
					func(
						_ context.Context,
						_ *agentv1.ResourceRequest,
						requestBody io.Reader,
					) (*agentv1.ResourceResponse, io.Reader, error) {
						body, err := io.ReadAll(requestBody)
						if err != nil {
							return nil, nil, err
						}
						return &agentv1.ResourceResponse{
							Result:   agentv1.ResultCode_RESULT_CODE_OK,
							BodySize: uint64(len(body)),
						}, bytes.NewReader(body), nil
					},
				),
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	serveContext, cancelServe := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- streamServer.Serve(serveContext, server)
	}()
	defer func() {
		cancelServe()
		_ = client.CloseWithError(0, "test complete")
		select {
		case err := <-serveDone:
			if err != nil {
				t.Errorf("stop Stream Server: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("Stream Server did not stop")
		}
	}()

	t.Run("unknown kind", func(t *testing.T) {
		stream := openTestStream(t, client)
		writeTestHeader(
			t,
			stream,
			agentv1.StreamKind_STREAM_KIND_POD_LOGS,
			"00000000-0000-4000-8000-000000000011",
		)
		requireRemoteStreamError(t, stream, StreamErrorUnsupported)
	})

	t.Run("oversized first frame", func(t *testing.T) {
		stream := openTestStream(t, client)
		var prefix [4]byte
		binary.BigEndian.PutUint32(prefix[:], MaxFrameSize+1)
		if _, err := stream.Write(prefix[:]); err != nil {
			t.Fatal(err)
		}
		requireRemoteStreamError(t, stream, StreamErrorProtocol)
	})

	t.Run("oversized body", func(t *testing.T) {
		stream := openTestStream(t, client)
		writeTestHeader(
			t,
			stream,
			agentv1.StreamKind_STREAM_KIND_RESOURCE,
			"00000000-0000-4000-8000-000000000012",
		)
		if err := WriteMessage(stream, &agentv1.ResourceRequest{
			Verb: agentv1.ResourceVerb_RESOURCE_VERB_UPDATE,
			Resource: &agentv1.GroupVersionResource{
				Version:  "v1",
				Resource: "pods",
			},
			Namespace: "default",
			Name:      "oversized",
			BodySize:  1025,
		}); err != nil {
			t.Fatal(err)
		}
		requireRemoteStreamError(t, stream, StreamErrorBodyTooLarge)
	})

	t.Run("truncated body", func(t *testing.T) {
		stream := openTestStream(t, client)
		writeTestHeaderWithIdempotency(
			t,
			stream,
			"00000000-0000-4000-8000-000000000013",
		)
		if err := WriteMessage(stream, &agentv1.ResourceRequest{
			Verb: agentv1.ResourceVerb_RESOURCE_VERB_UPDATE,
			Resource: &agentv1.GroupVersionResource{
				Version:  "v1",
				Resource: "pods",
			},
			Namespace: "default",
			Name:      "truncated",
			BodySize:  10,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := stream.Write([]byte("abc")); err != nil {
			t.Fatal(err)
		}
		if err := stream.Close(); err != nil {
			t.Fatal(err)
		}
		requireRemoteStreamError(t, stream, StreamErrorProtocol)
	})

	t.Run("header deadline", func(t *testing.T) {
		stream := openTestStream(t, client)
		// A QUIC Stream is visible to the peer only after its first STREAM
		// frame. Send an incomplete length prefix so AcceptStream observes it
		// and the first-message deadline can take effect.
		if _, err := stream.Write([]byte{0}); err != nil {
			t.Fatal(err)
		}
		requireRemoteStreamError(t, stream, StreamErrorProtocol)
	})

	response, err := DoResource(
		context.Background(),
		client,
		&agentv1.StreamHeader{
			ProtocolVersion: ProtocolVersion,
			Kind:            agentv1.StreamKind_STREAM_KIND_RESOURCE,
			RequestId:       "00000000-0000-4000-8000-000000000014",
			TimeoutMillis:   1000,
		},
		&agentv1.ResourceRequest{
			Verb: agentv1.ResourceVerb_RESOURCE_VERB_GET,
			Resource: &agentv1.GroupVersionResource{
				Version:  "v1",
				Resource: "pods",
			},
			Namespace: "default",
			Name:      "healthy",
		},
		nil,
		io.Discard,
		1024,
	)
	if err != nil {
		t.Fatalf("valid Stream after isolated failures: %v", err)
	}
	if response.GetResult() != agentv1.ResultCode_RESULT_CODE_OK {
		t.Fatalf("unexpected valid response: %+v", response)
	}

	requestBody := bytes.Repeat([]byte("zke-resource-body"), 32)
	var responseBody bytes.Buffer
	response, err = DoResource(
		context.Background(),
		client,
		&agentv1.StreamHeader{
			ProtocolVersion: ProtocolVersion,
			Kind:            agentv1.StreamKind_STREAM_KIND_RESOURCE,
			RequestId:       "00000000-0000-4000-8000-000000000015",
			TimeoutMillis:   1000,
			IdempotencyKey:  "resource-body-test",
		},
		&agentv1.ResourceRequest{
			Verb: agentv1.ResourceVerb_RESOURCE_VERB_UPDATE,
			Resource: &agentv1.GroupVersionResource{
				Version:  "v1",
				Resource: "configmaps",
			},
			Namespace: "default",
			Name:      "body",
			BodySize:  uint64(len(requestBody)),
		},
		bytes.NewReader(requestBody),
		&responseBody,
		1024,
	)
	if err != nil {
		t.Fatalf("stream Resource request and response bodies: %v", err)
	}
	if response.GetBodySize() != uint64(len(requestBody)) ||
		!bytes.Equal(responseBody.Bytes(), requestBody) {
		t.Fatal("Resource request or response body changed during streaming")
	}
}

func TestRealQUICConnectionCloseCancelsResourceHandler(t *testing.T) {
	client, server, stop := openStreamTestConnection(t)
	defer stop()
	started := make(chan struct{})
	canceled := make(chan struct{})
	streamServer, err := NewStreamServer(StreamServerConfig{
		HeaderTimeout: 200 * time.Millisecond,
		MaxTimeout:    5 * time.Second,
		Handlers: map[agentv1.StreamKind]StreamHandlerConfig{
			agentv1.StreamKind_STREAM_KIND_RESOURCE: {
				MaxConcurrent: 1,
				Handle: ResourceStreamHandler(
					1024,
					func(
						ctx context.Context,
						_ *agentv1.ResourceRequest,
						_ io.Reader,
					) (*agentv1.ResourceResponse, io.Reader, error) {
						close(started)
						<-ctx.Done()
						close(canceled)
						return nil, nil, ctx.Err()
					},
				),
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	serveContext, cancelServe := context.WithCancel(context.Background())
	defer cancelServe()
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- streamServer.Serve(serveContext, server)
	}()
	requestDone := make(chan error, 1)
	go func() {
		_, err := DoResource(
			context.Background(),
			client,
			&agentv1.StreamHeader{
				ProtocolVersion: ProtocolVersion,
				Kind:            agentv1.StreamKind_STREAM_KIND_RESOURCE,
				RequestId:       "00000000-0000-4000-8000-000000000021",
				TimeoutMillis:   5000,
			},
			&agentv1.ResourceRequest{
				Verb: agentv1.ResourceVerb_RESOURCE_VERB_GET,
				Resource: &agentv1.GroupVersionResource{
					Version:  "v1",
					Resource: "pods",
				},
				Namespace: "default",
				Name:      "connection-close",
			},
			nil,
			io.Discard,
			1024,
		)
		requestDone <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("Resource handler did not start")
	}
	if err := client.CloseWithError(0, "test disconnect"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("Connection close did not cancel the Resource handler")
	}
	select {
	case err := <-requestDone:
		if err == nil {
			t.Fatal("Resource request returned no error after Connection close")
		}
	case <-time.After(time.Second):
		t.Fatal("Resource request did not return after Connection close")
	}
	select {
	case <-serveDone:
	case <-time.After(time.Second):
		t.Fatal("Stream Server did not stop after Connection close")
	}
}

func openStreamTestConnection(
	t *testing.T,
) (*quic.Conn, *quic.Conn, func()) {
	t.Helper()
	certificate, roots := streamTestTLS(t)
	listener, err := quic.ListenAddr(
		"127.0.0.1:0",
		&tls.Config{
			MinVersion:   tls.VersionTLS13,
			Certificates: []tls.Certificate{certificate},
			NextProtos:   []string{"zke-stream-test"},
		},
		&quic.Config{
			MaxIncomingStreams:    16,
			MaxIncomingUniStreams: -1,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	acceptContext, cancelAccept := context.WithTimeout(
		context.Background(),
		2*time.Second,
	)
	serverConnection := make(chan *quic.Conn, 1)
	serverError := make(chan error, 1)
	go func() {
		connection, err := listener.Accept(acceptContext)
		if err != nil {
			serverError <- err
			return
		}
		serverConnection <- connection
	}()
	client, err := quic.DialAddr(
		acceptContext,
		listener.Addr().String(),
		&tls.Config{
			MinVersion: tls.VersionTLS13,
			ServerName: "127.0.0.1",
			RootCAs:    roots,
			NextProtos: []string{"zke-stream-test"},
		},
		&quic.Config{
			MaxIncomingStreams:    16,
			MaxIncomingUniStreams: -1,
		},
	)
	if err != nil {
		cancelAccept()
		listener.Close()
		t.Fatal(err)
	}
	var server *quic.Conn
	select {
	case server = <-serverConnection:
	case err := <-serverError:
		cancelAccept()
		listener.Close()
		t.Fatal(err)
	case <-acceptContext.Done():
		cancelAccept()
		listener.Close()
		t.Fatal("timed out accepting real QUIC test connection")
	}
	cancelAccept()
	return client, server, func() {
		_ = client.CloseWithError(0, "test complete")
		_ = server.CloseWithError(0, "test complete")
		_ = listener.Close()
	}
}

func streamTestTLS(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: "127.0.0.1",
		},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	certificateDER, err := x509.CreateCertificate(
		rand.Reader,
		template,
		template,
		&privateKey.PublicKey,
		privateKey,
	)
	if err != nil {
		t.Fatal(err)
	}
	certificate := tls.Certificate{
		Certificate: [][]byte{certificateDER},
		PrivateKey:  privateKey,
	}
	parsed, err := x509.ParseCertificate(certificateDER)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(parsed)
	return certificate, roots
}

func openTestStream(t *testing.T, connection *quic.Conn) *quic.Stream {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	stream, err := connection.OpenStreamSync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	return stream
}

func writeTestHeader(
	t *testing.T,
	stream *quic.Stream,
	kind agentv1.StreamKind,
	requestID string,
) {
	t.Helper()
	if err := WriteMessage(stream, &agentv1.StreamHeader{
		ProtocolVersion: ProtocolVersion,
		Kind:            kind,
		RequestId:       requestID,
		TimeoutMillis:   1000,
	}); err != nil {
		t.Fatal(err)
	}
}

func writeTestHeaderWithIdempotency(
	t *testing.T,
	stream *quic.Stream,
	requestID string,
) {
	t.Helper()
	if err := WriteMessage(stream, &agentv1.StreamHeader{
		ProtocolVersion: ProtocolVersion,
		Kind:            agentv1.StreamKind_STREAM_KIND_RESOURCE,
		RequestId:       requestID,
		TimeoutMillis:   1000,
		IdempotencyKey:  "test-idempotency-key",
	}); err != nil {
		t.Fatal(err)
	}
}

func requireRemoteStreamError(
	t *testing.T,
	stream *quic.Stream,
	code quic.StreamErrorCode,
) {
	t.Helper()
	var buffer [1]byte
	_, err := stream.Read(buffer[:])
	if err == nil {
		t.Fatalf("Stream read succeeded, want reset code %d", code)
	}
	var streamError *quic.StreamError
	if !errors.As(err, &streamError) ||
		!streamError.Remote ||
		streamError.ErrorCode != code {
		t.Fatalf("Stream error = %v, want remote reset code %d", err, code)
	}
}

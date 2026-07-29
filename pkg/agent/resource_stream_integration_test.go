package agent

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/server/agentconn"
	"github.com/togettoyou/zke/pkg/server/enrollment"
	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
)

func TestResourceStreamsMultiplexSlowAndSmallResponses(t *testing.T) {
	const (
		smallRequests = 100
		slowBodySize  = 4 * 1024 * 1024
	)
	slowStarted := make(chan struct{})
	var startSlow sync.Once
	handler := func(
		ctx context.Context,
		request *agentv1.ResourceRequest,
		_ io.Reader,
	) (*agentv1.ResourceResponse, io.Reader, error) {
		if request.GetName() == "slow" {
			startSlow.Do(func() { close(slowStarted) })
			return &agentv1.ResourceResponse{
				Result:      agentv1.ResultCode_RESULT_CODE_OK,
				ContentType: "application/octet-stream",
				BodySize:    slowBodySize,
			}, io.LimitReader(zeroReader{}, slowBodySize), nil
		}
		body := []byte(`{"ok":true}`)
		return &agentv1.ResourceResponse{
			Result:      agentv1.ResultCode_RESULT_CODE_OK,
			ContentType: "application/json",
			BodySize:    uint64(len(body)),
		}, bytes.NewReader(body), nil
	}
	environment := startResourceStreamEnvironment(t, handler, resourceTestLimits{
		agentIncoming:   256,
		agentResource:   128,
		serverResource:  128,
		serverTotal:     256,
		maxBodyBytes:    8 * 1024 * 1024,
		resourceTimeout: 15 * time.Second,
	})

	slowDone := make(chan error, 1)
	go func() {
		var output slowWriter
		_, err := environment.manager.RequestResource(
			environment.ctx,
			testClusterID,
			resourceGetRequest("slow"),
			nil,
			&output,
		)
		slowDone <- err
	}()
	select {
	case <-slowStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("slow Resource Stream did not start")
	}

	started := time.Now()
	errorsFound := make(chan error, smallRequests)
	var requests sync.WaitGroup
	requests.Add(smallRequests)
	for range smallRequests {
		go func() {
			defer requests.Done()
			var output bytes.Buffer
			response, err := environment.manager.RequestResource(
				environment.ctx,
				testClusterID,
				resourceGetRequest("small"),
				nil,
				&output,
			)
			if err != nil {
				errorsFound <- err
				return
			}
			if response.GetResult() != agentv1.ResultCode_RESULT_CODE_OK ||
				output.String() != `{"ok":true}` {
				errorsFound <- errors.New("unexpected small Resource response")
			}
		}()
	}
	requestsDone := make(chan struct{})
	go func() {
		requests.Wait()
		close(requestsDone)
	}()
	select {
	case <-requestsDone:
	case <-time.After(4 * time.Second):
		t.Fatal("small Resource Streams waited behind the slow response")
	}
	close(errorsFound)
	for err := range errorsFound {
		t.Error(err)
	}
	if elapsed := time.Since(started); elapsed >= 2*time.Second {
		t.Fatalf("small Resource Streams took %s while a slow Stream was active", elapsed)
	}
	select {
	case err := <-slowDone:
		t.Fatalf("slow Resource Stream finished before small requests: %v", err)
	default:
	}

	heartbeatDeadline := time.After(3 * time.Second)
	for environment.store.heartbeats.Load() == 0 {
		select {
		case err := <-slowDone:
			t.Fatalf("slow Resource Stream ended before a heartbeat: %v", err)
		case <-heartbeatDeadline:
			t.Fatal("Control Stream heartbeat stopped during the slow Resource Stream")
		case <-time.After(20 * time.Millisecond):
		}
	}
	select {
	case err := <-slowDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("slow Resource Stream did not finish")
	}
}

func TestResourceStreamCancellationIsIsolated(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	var startOnce, cancelOnce sync.Once
	handler := func(
		ctx context.Context,
		request *agentv1.ResourceRequest,
		_ io.Reader,
	) (*agentv1.ResourceResponse, io.Reader, error) {
		if request.GetName() == "cancel" {
			startOnce.Do(func() { close(started) })
			<-ctx.Done()
			cancelOnce.Do(func() { close(canceled) })
			return nil, nil, ctx.Err()
		}
		return &agentv1.ResourceResponse{
			Result: agentv1.ResultCode_RESULT_CODE_OK,
		}, nil, nil
	}
	environment := startResourceStreamEnvironment(
		t,
		handler,
		defaultResourceTestLimits(),
	)
	requestContext, cancelRequest := context.WithCancel(environment.ctx)
	requestDone := make(chan error, 1)
	go func() {
		_, err := environment.manager.RequestResource(
			requestContext,
			testClusterID,
			resourceGetRequest("cancel"),
			nil,
			io.Discard,
		)
		requestDone <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("cancel Resource Stream did not start")
	}
	cancelRequest()
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("Agent Resource handler Context was not canceled")
	}
	select {
	case err := <-requestDone:
		if err == nil {
			t.Fatal("canceled Resource request returned no error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Server Resource request did not return after cancellation")
	}

	response, err := environment.manager.RequestResource(
		environment.ctx,
		testClusterID,
		resourceGetRequest("still-works"),
		nil,
		io.Discard,
	)
	if err != nil {
		t.Fatal(err)
	}
	if response.GetResult() != agentv1.ResultCode_RESULT_CODE_OK {
		t.Fatalf("unexpected response after isolated cancellation: %+v", response)
	}
}

func TestResourceStreamOpenHonorsQUICIncomingLimit(t *testing.T) {
	blocked := make(chan struct{})
	releaseFirst := make(chan struct{})
	var first sync.Once
	handler := func(
		ctx context.Context,
		_ *agentv1.ResourceRequest,
		_ io.Reader,
	) (*agentv1.ResourceResponse, io.Reader, error) {
		isFirst := false
		first.Do(func() {
			isFirst = true
			close(blocked)
		})
		if isFirst {
			select {
			case <-releaseFirst:
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			}
		}
		return &agentv1.ResourceResponse{
			Result: agentv1.ResultCode_RESULT_CODE_OK,
		}, nil, nil
	}
	limits := defaultResourceTestLimits()
	limits.agentIncoming = 1
	limits.agentResource = 1
	environment := startResourceStreamEnvironment(t, handler, limits)

	firstDone := make(chan error, 1)
	go func() {
		_, err := environment.manager.RequestResource(
			environment.ctx,
			testClusterID,
			resourceGetRequest("first"),
			nil,
			io.Discard,
		)
		firstDone <- err
	}()
	select {
	case <-blocked:
	case <-time.After(2 * time.Second):
		t.Fatal("first Resource Stream did not occupy the incoming quota")
	}

	secondContext, cancelSecond := context.WithTimeout(
		environment.ctx,
		150*time.Millisecond,
	)
	defer cancelSecond()
	startedAt := time.Now()
	_, err := environment.manager.RequestResource(
		secondContext,
		testClusterID,
		resourceGetRequest("second"),
		nil,
		io.Discard,
	)
	if err == nil {
		t.Fatal("second Resource Stream opened above the QUIC incoming limit")
	}
	if time.Since(startedAt) < 100*time.Millisecond {
		t.Fatalf("second Resource Stream failed before its open deadline: %v", err)
	}
	close(releaseFirst)
	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first Resource Stream did not finish")
	}
}

func TestResourceStreamApplicationQuotaRejectsOnlyExcessStream(t *testing.T) {
	blocked := make(chan struct{})
	releaseFirst := make(chan struct{})
	var calls atomic.Uint64
	handler := func(
		ctx context.Context,
		_ *agentv1.ResourceRequest,
		_ io.Reader,
	) (*agentv1.ResourceResponse, io.Reader, error) {
		if calls.Add(1) == 1 {
			close(blocked)
			select {
			case <-releaseFirst:
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			}
		}
		return &agentv1.ResourceResponse{
			Result: agentv1.ResultCode_RESULT_CODE_OK,
		}, nil, nil
	}
	limits := defaultResourceTestLimits()
	limits.agentResource = 1
	environment := startResourceStreamEnvironment(t, handler, limits)

	firstDone := make(chan error, 1)
	go func() {
		_, err := environment.manager.RequestResource(
			environment.ctx,
			testClusterID,
			resourceGetRequest("first"),
			nil,
			io.Discard,
		)
		firstDone <- err
	}()
	select {
	case <-blocked:
	case <-time.After(2 * time.Second):
		t.Fatal("first Resource handler did not occupy the application quota")
	}

	_, err := environment.manager.RequestResource(
		environment.ctx,
		testClusterID,
		resourceGetRequest("excess"),
		nil,
		io.Discard,
	)
	if err == nil {
		t.Fatal("excess Resource Stream was not rejected")
	}
	var streamError *quic.StreamError
	if !errors.As(err, &streamError) ||
		streamError.ErrorCode != agentprotocol.StreamErrorResourceExhausted {
		t.Fatalf("excess Stream error = %v, want remote resource exhaustion", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("Resource handler calls = %d, want 1", calls.Load())
	}

	close(releaseFirst)
	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first Resource Stream did not finish")
	}
	response, err := environment.manager.RequestResource(
		environment.ctx,
		testClusterID,
		resourceGetRequest("after-release"),
		nil,
		io.Discard,
	)
	if err != nil {
		t.Fatal(err)
	}
	if response.GetResult() != agentv1.ResultCode_RESULT_CODE_OK {
		t.Fatalf("unexpected response after quota release: %+v", response)
	}
}

type resourceStreamEnvironment struct {
	ctx     context.Context
	manager *agentconn.Manager
	store   *resourceTestConnectionStore
}

type resourceTestLimits struct {
	agentIncoming   int64
	agentResource   int
	serverResource  int
	serverTotal     int
	maxBodyBytes    uint64
	resourceTimeout time.Duration
}

func defaultResourceTestLimits() resourceTestLimits {
	return resourceTestLimits{
		agentIncoming:   16,
		agentResource:   8,
		serverResource:  8,
		serverTotal:     32,
		maxBodyBytes:    1024 * 1024,
		resourceTimeout: 5 * time.Second,
	}
}

func startResourceStreamEnvironment(
	t *testing.T,
	handler agentprotocol.ResourceHandler,
	limits resourceTestLimits,
) resourceStreamEnvironment {
	t.Helper()
	now := time.Now().UTC()
	_, _, clientCAPEM, clientCAKeyPEM :=
		createConnectionTestCA(t, "Resource Test Agent Client CA", 101, now)
	_, listenerCAPrivateKey, listenerCAPEM, _ :=
		createConnectionTestCA(t, "Resource Test Agent Listener CA", 102, now)
	listenerCertificatePEM, listenerPrivateKeyPEM :=
		createConnectionTestListenerCertificate(
			t,
			listenerCAPEM,
			listenerCAPrivateKey,
			now,
		)

	pending, err := newPendingIdentity()
	if err != nil {
		t.Fatal(err)
	}
	csrBlock, _ := pem.Decode(pending.CSRPEM)
	if csrBlock == nil {
		t.Fatal("decode Resource test Agent CSR")
	}
	certificateRequest, err := x509.ParseCertificateRequest(csrBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := enrollment.NewCertificateSigner(
		clientCAPEM,
		clientCAKeyPEM,
		24*time.Hour,
	)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := signer.Sign(
		certificateRequest,
		enrollment.CertificateIdentity{
			TenantID:  testTenantID,
			ProjectID: testProjectID,
			ClusterID: testClusterID,
			AgentID:   testAgentID,
		},
		now,
	)
	if err != nil {
		t.Fatal(err)
	}
	identity := LocalIdentity{
		ClusterID:            testClusterID,
		AgentID:              testAgentID,
		PrivateKeyPEM:        pending.PrivateKeyPEM,
		CertificatePEM:       []byte(signed.PEM),
		CertificateExpiresAt: signed.ExpiresAt,
	}

	certificateDirectory := t.TempDir()
	listenerCertificateFile := filepath.Join(certificateDirectory, "listener.crt")
	listenerPrivateKeyFile := filepath.Join(certificateDirectory, "listener.key")
	listenerCAFile := filepath.Join(certificateDirectory, "listener-ca.crt")
	clientCAFile := filepath.Join(certificateDirectory, "client-ca.crt")
	for path, content := range map[string][]byte{
		listenerCertificateFile: listenerCertificatePEM,
		listenerPrivateKeyFile:  listenerPrivateKeyPEM,
		listenerCAFile:          listenerCAPEM,
		clientCAFile:            clientCAPEM,
	} {
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	connectionStore := &resourceTestConnectionStore{}
	address := reserveUDPAddress(t)
	manager, err := agentconn.New(
		agentconn.Config{
			Address:                 address,
			TLSCertificateFile:      listenerCertificateFile,
			TLSPrivateKeyFile:       listenerPrivateKeyFile,
			ClientCACertificateFile: clientCAFile,
			HandshakeTimeout:        2 * time.Second,
			HeartbeatInterval:       time.Second,
			HeartbeatTimeout:        5 * time.Second,
			LastSeenWriteInterval:   time.Second,
			OperationTimeout:        time.Second,
			WriteTimeout:            time.Second,
			MaxConcurrentAgents:     4,
			MaxIncomingStreams:      16,
			ResourceRequestTimeout:  limits.resourceTimeout,
			MaxResourceBodyBytes:    limits.maxBodyBytes,
			MaxResourceStreams:      limits.serverResource,
			MaxResourceRequests:     limits.serverTotal,
		},
		logger,
		connectionStore,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	runContext, cancelRun := context.WithCancel(context.Background())
	managerDone := make(chan error, 1)
	go func() {
		managerDone <- manager.Run(runContext)
	}()
	agentDone := make(chan error, 1)
	go func() {
		agentDone <- runConnectionLoopWithServices(
			runContext,
			Config{
				CertificateRenewBefore: time.Hour,
				Connection: ConnectionConfig{
					ServerAddress:                address,
					CACertificateFile:            listenerCAFile,
					ConnectTimeout:               time.Second,
					RetryInitialInterval:         10 * time.Millisecond,
					RetryMaxInterval:             50 * time.Millisecond,
					IdleTimeout:                  time.Minute,
					KeepAliveInterval:            time.Second,
					MaxIncomingStreams:           limits.agentIncoming,
					StreamHeaderTimeout:          time.Second,
					MaxResourceRequestTimeout:    limits.resourceTimeout,
					MaxConcurrentResourceStreams: limits.agentResource,
					MaxResourceBodyBytes:         limits.maxBodyBytes,
				},
			},
			nil,
			identity,
			"resource-test",
			"00000000-0000-4000-8000-000000000099",
			logger,
			connectionServices{resourceHandler: handler},
		)
	}()

	deadline := time.After(4 * time.Second)
	for manager.Snapshot([]string{testAgentID})[testAgentID].State !=
		agentconn.ConnectionStateOnline {
		select {
		case err := <-managerDone:
			t.Fatalf("Resource test Server stopped before Agent connected: %v", err)
		case err := <-agentDone:
			t.Fatalf("Resource test Agent stopped before connecting: %v", err)
		case <-deadline:
			t.Fatal("timed out waiting for Resource test Agent connection")
		case <-time.After(10 * time.Millisecond):
		}
	}
	t.Cleanup(func() {
		cancelRun()
		select {
		case err := <-agentDone:
			if err != nil {
				t.Errorf("stop Resource test Agent: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("Resource test Agent did not stop")
		}
		select {
		case err := <-managerDone:
			if err != nil {
				t.Errorf("stop Resource test Server: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("Resource test Server did not stop")
		}
	})
	return resourceStreamEnvironment{
		ctx:     runContext,
		manager: manager,
		store:   connectionStore,
	}
}

func resourceGetRequest(name string) *agentv1.ResourceRequest {
	return &agentv1.ResourceRequest{
		Verb: agentv1.ResourceVerb_RESOURCE_VERB_GET,
		Resource: &agentv1.GroupVersionResource{
			Version:  "v1",
			Resource: "pods",
		},
		Namespace:      "default",
		Name:           name,
		Representation: agentv1.ResourceRepresentation_RESOURCE_REPRESENTATION_FULL_OBJECT,
	}
}

type resourceTestConnectionStore struct {
	heartbeats atomic.Uint64
}

func (connectionStore *resourceTestConnectionStore) Activate(
	context.Context,
	store.ActivateAgentConnectionParams,
) error {
	return nil
}

func (connectionStore *resourceTestConnectionStore) RecordHeartbeat(
	context.Context,
	store.RecordAgentHeartbeatParams,
) error {
	connectionStore.heartbeats.Add(1)
	return nil
}

func (connectionStore *resourceTestConnectionStore) WatchRevocations(
	ctx context.Context,
	onReady func(),
	_ func(store.AgentConnectionRevocation),
) error {
	onReady()
	<-ctx.Done()
	return nil
}

type zeroReader struct{}

func (zeroReader) Read(buffer []byte) (int, error) {
	for index := range buffer {
		buffer[index] = 0
	}
	return len(buffer), nil
}

type slowWriter struct {
	written int64
}

func (writer *slowWriter) Write(buffer []byte) (int, error) {
	time.Sleep(20 * time.Millisecond)
	writer.written += int64(len(buffer))
	return len(buffer), nil
}

package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/server/auth"
	"github.com/togettoyou/zke/pkg/server/podexec"
	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
)

func TestKubernetesPodExecCreateBindsIdentityTargetAndConfirmation(t *testing.T) {
	t.Parallel()

	service := &fakePodExecHTTPService{session: testHTTPSession()}
	handler := newKubernetesPodExecHandler(
		slog.New(slog.NewTextHandler(io.Discard, nil)), service, nil, nil, nil,
		time.Second, PodExecHTTPConfig{},
	)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(
		`{"uid":"pod-uid","container":"main","columns":120,"rows":40,"confirm":true}`,
	))
	context.Request.Header.Set("Content-Type", "application/json")
	context.Request.Header.Set(idempotencyKeyHeaderName, "terminal-session-0001")
	context.Params = gin.Params{
		{Key: "cluster_id", Value: "00000000-0000-4000-8000-000000000003"},
		{Key: "namespace_name", Value: "workloads"},
		{Key: "pod_name", Value: "api-0"},
	}
	context.Set("authenticated_identity", auth.Identity{
		User:      auth.User{ID: "00000000-0000-4000-8000-000000000001"},
		SessionID: "00000000-0000-4000-8000-000000000002",
	})

	handler.create(context)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	input := service.createInput
	if input.UserID != "00000000-0000-4000-8000-000000000001" ||
		input.AuthSessionID != "00000000-0000-4000-8000-000000000002" ||
		input.ClusterID != "00000000-0000-4000-8000-000000000003" ||
		input.Namespace != "workloads" || input.PodName != "api-0" ||
		input.PodUID != "pod-uid" || input.Container != "main" ||
		input.IdempotencyKey != "terminal-session-0001" || !input.Confirm {
		t.Fatalf("unexpected Create input: %+v", input)
	}
}

func TestKubernetesPodExecWebSocketUsesProtocolFrames(t *testing.T) {
	service := &fakePodExecHTTPService{session: testHTTPSession()}
	handler := newKubernetesPodExecHandler(
		slog.New(slog.NewTextHandler(io.Discard, nil)), service, nil, nil, nil,
		time.Second,
		PodExecHTTPConfig{MaximumDuration: time.Second, RevalidateInterval: time.Second},
	)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET(
		"/api/v1/clusters/:cluster_id/namespaces/:namespace_name/pods/:pod_name/terminal-sessions/:session_id",
		func(c *gin.Context) {
			c.Set("authenticated_identity", auth.Identity{
				User:      auth.User{ID: "00000000-0000-4000-8000-000000000001"},
				SessionID: "00000000-0000-4000-8000-000000000002",
			})
			c.Set("authenticated_session_token", "session-token")
			c.Next()
		},
		handler.connect,
	)
	server := httptest.NewServer(router)
	defer server.Close()
	webSocketURL := "ws" + strings.TrimPrefix(server.URL, "http") +
		"/api/v1/clusters/00000000-0000-4000-8000-000000000003/namespaces/workloads/pods/api-0/terminal-sessions/00000000-0000-4000-8000-000000000010"
	dialer := websocket.Dialer{Subprotocols: []string{podExecWebSocketProtocol}}
	connection, response, err := dialer.Dial(webSocketURL, http.Header{"Origin": []string{server.URL}})
	if response != nil {
		defer response.Body.Close()
	}
	if err != nil {
		if response != nil {
			t.Fatalf("dial status=%d err=%v", response.StatusCode, err)
		}
		t.Fatal(err)
	}
	defer func() { _ = connection.Close() }()
	if connection.Subprotocol() != podExecWebSocketProtocol {
		t.Fatalf("subprotocol = %q", connection.Subprotocol())
	}
	for index, wantType := range []string{"stdout", "exit"} {
		message := podExecWireMessage{}
		if err := connection.ReadJSON(&message); err != nil {
			t.Fatalf("read message %d: %v", index, err)
		}
		if message.Type != wantType {
			t.Fatalf("message %d = %+v", index, message)
		}
		if wantType == "stdout" && string(message.Data) != "ready\n" {
			t.Fatalf("stdout = %q", message.Data)
		}
		if wantType == "exit" && (message.Result != "ok" || message.ExitCode != 7) {
			t.Fatalf("exit = %+v", message)
		}
	}
	if service.consumeInput.AuthSessionID != "00000000-0000-4000-8000-000000000002" ||
		service.consumeInput.PodName != "api-0" {
		t.Fatalf("unexpected Consume input: %+v", service.consumeInput)
	}
}

func TestPodExecPeerSendsWebSocketPing(t *testing.T) {
	pingSent := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		connection, err := (&websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}).Upgrade(writer, request, nil)
		if err != nil {
			pingSent <- err
			return
		}
		defer connection.Close()
		peer := &podExecWebSocketPeer{connection: connection, writeTimeout: time.Second}
		pingSent <- peer.ping()
	}))
	defer server.Close()

	connection, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	pingReceived := make(chan struct{}, 1)
	connection.SetPingHandler(func(data string) error {
		pingReceived <- struct{}{}
		return connection.WriteControl(websocket.PongMessage, []byte(data), time.Now().Add(time.Second))
	})
	_ = connection.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, _ = connection.ReadMessage()

	select {
	case <-pingReceived:
	case <-time.After(2 * time.Second):
		t.Fatal("client did not receive WebSocket ping")
	}
	if err := <-pingSent; err != nil {
		t.Fatalf("ping() error = %v", err)
	}
}

func TestPodExecSameOriginRequiresExactHost(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		origin string
		valid  bool
	}{
		{"https://zke.example.com", true},
		{"http://zke.example.com", false},
		{"https://evil.example.com", false},
		{"null", false},
		{"", false},
	} {
		request := httptest.NewRequest(http.MethodGet, "https://zke.example.com/terminal", nil)
		request.Header.Set("Origin", test.origin)
		if got := podExecSameOrigin(request); got != test.valid {
			t.Fatalf("origin %q valid=%t, want %t", test.origin, got, test.valid)
		}
	}
}

type fakePodExecHTTPService struct {
	createInput  podexec.CreateInput
	consumeInput podexec.ConsumeInput
	session      podexec.Session
}

func (service *fakePodExecHTTPService) Create(input podexec.CreateInput) (podexec.Session, error) {
	service.createInput = input
	return service.session, nil
}

func (service *fakePodExecHTTPService) Consume(input podexec.ConsumeInput) (podexec.Session, error) {
	service.consumeInput = input
	return service.session, nil
}

func (service *fakePodExecHTTPService) ConsumeBound(input podexec.ConsumeInput) (podexec.Session, error) {
	service.consumeInput = input
	return service.session, nil
}

func (*fakePodExecHTTPService) Run(
	ctx context.Context,
	_ podexec.Session,
	peer agentprotocol.PodExecPeer,
) (podexec.Result, error) {
	if err := peer.Send(ctx, &agentv1.PodExecFrame{Message: &agentv1.PodExecFrame_Output{
		Output: &agentv1.PodExecOutput{
			Stream: agentv1.PodExecOutputStream_POD_EXEC_OUTPUT_STREAM_STDOUT,
			Data:   []byte("ready\n"),
		},
	}}); err != nil {
		return podexec.Result{}, err
	}
	if err := peer.Send(ctx, &agentv1.PodExecFrame{Message: &agentv1.PodExecFrame_Exit{
		Exit: &agentv1.PodExecExit{
			Result: agentv1.ResultCode_RESULT_CODE_OK, ExitCode: 7, OutputBytes: 6,
		},
	}}); err != nil {
		return podexec.Result{}, err
	}
	return podexec.Result{ExitCode: 7, OutputBytes: 6}, nil
}

func (*fakePodExecHTTPService) ListRecordings(
	context.Context,
	podexec.RecordingScope,
) ([]podexec.Recording, error) {
	return nil, nil
}

func (*fakePodExecHTTPService) GetRecording(
	context.Context,
	podexec.RecordingScope,
	string,
) (podexec.Recording, error) {
	return podexec.Recording{}, podexec.ErrRecordingNotFound
}

func testHTTPSession() podexec.Session {
	return podexec.Session{
		ID:            "00000000-0000-4000-8000-000000000010",
		UserID:        "00000000-0000-4000-8000-000000000001",
		AuthSessionID: "00000000-0000-4000-8000-000000000002",
		ClusterID:     "00000000-0000-4000-8000-000000000003",
		Namespace:     "workloads", PodName: "api-0", PodUID: "pod-uid", Container: "main",
		Columns: 120, Rows: 40,
		ExpiresAt: time.Now().Add(time.Minute),
	}
}

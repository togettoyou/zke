package podaccess

import (
	"bytes"
	"context"
	"crypto/sha1" //nolint:gosec // SHA-1 is fixed by RFC 6455 for the WebSocket handshake, not used for security.
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/togettoyou/zke/pkg/server/auth"
	"github.com/togettoyou/zke/pkg/server/podportforward"
	"github.com/togettoyou/zke/pkg/server/rbac"
	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
)

const webSocketMagic = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

const (
	testUserID       = "00000000-0000-4000-8000-000000000001"
	testAuthID       = "00000000-0000-4000-8000-000000000002"
	testClusterID    = "00000000-0000-4000-8000-000000000003"
	testRequestID    = "00000000-0000-4000-8000-000000000004"
	testSessionToken = "login-session-token"
)

type testAuthenticator struct {
	mutex   sync.Mutex
	allowed bool
}

func (service *testAuthenticator) Authenticate(context.Context, string, time.Time) (auth.Identity, error) {
	service.mutex.Lock()
	defer service.mutex.Unlock()
	if !service.allowed {
		return auth.Identity{}, auth.ErrUnauthenticated
	}
	return auth.Identity{User: auth.User{ID: testUserID}, SessionID: testAuthID}, nil
}

func (service *testAuthenticator) revoke() {
	service.mutex.Lock()
	service.allowed = false
	service.mutex.Unlock()
}

type testAuthorizer struct{}

func (testAuthorizer) AuthorizeCluster(context.Context, string, rbac.Permission, string) (rbac.ResolvedScope, error) {
	return rbac.ResolvedScope{}, nil
}

type httpForwarder struct {
	requests chan string
}

type webSocketForwarder struct{}

func (webSocketForwarder) Run(ctx context.Context, _ podportforward.Session,
	peer agentprotocol.PodPortForwardPeer) (podportforward.Result, error) {
	var request bytes.Buffer
	for !strings.Contains(request.String(), "\r\n\r\n") {
		data, err := peer.Read(ctx)
		if err != nil {
			return podportforward.Result{}, err
		}
		request.Write(data)
	}
	key := ""
	for _, line := range strings.Split(request.String(), "\r\n") {
		if name, value, found := strings.Cut(line, ":"); found && strings.EqualFold(name, "Sec-WebSocket-Key") {
			key = strings.TrimSpace(value)
			break
		}
	}
	if key == "" {
		return podportforward.Result{}, errors.New("WebSocket key missing")
	}
	acceptDigest := sha1.Sum([]byte(key + webSocketMagic)) //nolint:gosec // Required by the WebSocket protocol.
	response := "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " +
		base64.StdEncoding.EncodeToString(acceptDigest[:]) + "\r\n\r\n"
	if err := peer.Write(ctx, []byte(response)); err != nil {
		return podportforward.Result{}, err
	}
	frame, err := readWebSocketFrame(ctx, peer)
	if err != nil {
		return podportforward.Result{}, err
	}
	if err := peer.Write(ctx, append([]byte{0x81, byte(len(frame))}, frame...)); err != nil {
		return podportforward.Result{}, err
	}
	return podportforward.Result{}, nil
}

func readWebSocketFrame(ctx context.Context, peer agentprotocol.PodPortForwardPeer) ([]byte, error) {
	var frame []byte
	for len(frame) < 6 || len(frame) < 6+int(frame[1]&0x7f) {
		data, err := peer.Read(ctx)
		if err != nil {
			return nil, err
		}
		frame = append(frame, data...)
		if len(frame) >= 2 && frame[1]&0x7f > 125 {
			return nil, errors.New("test WebSocket frame is too large")
		}
	}
	length := int(frame[1] & 0x7f)
	mask := frame[2:6]
	payload := append([]byte(nil), frame[6:6+length]...)
	for index := range payload {
		payload[index] ^= mask[index%len(mask)]
	}
	return payload, nil
}

func (forwarder *httpForwarder) Run(ctx context.Context, _ podportforward.Session,
	peer agentprotocol.PodPortForwardPeer) (podportforward.Result, error) {
	var request bytes.Buffer
	for !strings.Contains(request.String(), "\r\n\r\n") {
		data, err := peer.Read(ctx)
		if err != nil {
			return podportforward.Result{}, err
		}
		request.Write(data)
	}
	select {
	case forwarder.requests <- request.String():
	case <-ctx.Done():
		return podportforward.Result{}, ctx.Err()
	}
	response := "HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nSet-Cookie: app_session=abc; Path=/; HttpOnly\r\nClear-Site-Data: \"cookies\"\r\nStrict-Transport-Security: max-age=31536000\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok"
	if err := peer.Write(ctx, []byte(response)); err != nil {
		return podportforward.Result{}, err
	}
	return podportforward.Result{}, nil
}

func TestAccessActivationProxyAndCookieIsolation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	authenticator := &testAuthenticator{allowed: true}
	forwarder := &httpForwarder{requests: make(chan string, 2)}
	service, err := NewService(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), authenticator,
		testAuthorizer{}, nil, forwarder, Config{
			Enabled: true, ExternalURL: "http://127.0.0.1:8081", ActivationTTL: time.Minute,
			SessionTTL: time.Minute, RevalidateInterval: time.Minute, OperationTimeout: time.Second,
			MaxPending: 4, MaxActive: 4, MaxConnections: 4, MaxConnectionsPerSession: 2,
			MaxClientBytes: 1024 * 1024, MaxPodBytes: 1024 * 1024,
		})
	if err != nil {
		t.Fatal(err)
	}
	ticket, err := service.Create(validCreateInput(time.Now().UTC()))
	if err != nil {
		t.Fatal(err)
	}
	activationURL, _ := url.Parse(ticket.AccessURL)
	activationRequest := httptest.NewRequest(http.MethodGet, ticket.AccessURL, nil)
	activationRequest.Host = activationURL.Host
	activationResponse := httptest.NewRecorder()
	service.ServeHTTP(activationResponse, activationRequest)
	if activationResponse.Code != http.StatusSeeOther {
		t.Fatalf("activation status = %d, want 303; body=%s", activationResponse.Code, activationResponse.Body.String())
	}
	accessCookie := responseCookie(t, activationResponse.Result(), accessCookieName)
	replacementInput := validCreateInput(time.Now().UTC())
	replacementInput.IdempotencyKey = "pod-access-test-2"
	replacementTicket, err := service.Create(replacementInput)
	if err != nil {
		t.Fatal(err)
	}
	replacementURL, _ := url.Parse(replacementTicket.AccessURL)
	replacementRequest := httptest.NewRequest(http.MethodGet, replacementTicket.AccessURL, nil)
	replacementRequest.Host = replacementURL.Host
	replacementRequest.AddCookie(accessCookie)
	replacementResponse := httptest.NewRecorder()
	service.ServeHTTP(replacementResponse, replacementRequest)
	if replacementResponse.Code != http.StatusConflict {
		t.Fatalf("replacement status = %d, want 409", replacementResponse.Code)
	}
	privacyRequest := httptest.NewRequest(http.MethodGet, replacementTicket.AccessURL, nil)
	privacyRequest.Host = replacementURL.Host
	privacyResponse := httptest.NewRecorder()
	service.ServeHTTP(privacyResponse, privacyRequest)
	if privacyResponse.Code != http.StatusSeeOther {
		t.Fatalf("privacy activation after conflict = %d, want 303", privacyResponse.Code)
	}

	reusedResponse := httptest.NewRecorder()
	reusedRequest := httptest.NewRequest(http.MethodGet, ticket.AccessURL, nil)
	reusedRequest.Host = activationURL.Host
	service.ServeHTTP(reusedResponse, reusedRequest)
	if reusedResponse.Code != http.StatusGone {
		t.Fatalf("reused activation status = %d, want 410", reusedResponse.Code)
	}

	proxyRequest := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8081/dashboard", nil)
	proxyRequest.Host = activationURL.Host
	proxyRequest.AddCookie(accessCookie)
	proxyRequest.AddCookie(&http.Cookie{Name: "zke_session", Value: "must-not-reach-pod"})
	proxyRequest.Header.Set("Authorization", "Bearer application-token")
	proxyResponse := httptest.NewRecorder()
	service.ServeHTTP(proxyResponse, proxyRequest)
	if proxyResponse.Code != http.StatusOK || proxyResponse.Body.String() != "ok" {
		t.Fatalf("proxy response = %d %q", proxyResponse.Code, proxyResponse.Body.String())
	}
	if proxyResponse.Header().Get("Clear-Site-Data") != "" || proxyResponse.Header().Get("Strict-Transport-Security") != "" {
		t.Fatalf("shared-host state headers reached the browser: %v", proxyResponse.Header())
	}
	firstUpstream := <-forwarder.requests
	if strings.Contains(firstUpstream, "zke_session") || !strings.Contains(firstUpstream, "Authorization: Bearer application-token") {
		t.Fatalf("unexpected upstream request:\n%s", firstUpstream)
	}
	applicationCookie := responseCookieWithPrefix(t, proxyResponse.Result(), "zke_pa_")
	if applicationCookie.Name == "app_session" || !strings.HasSuffix(applicationCookie.Name, "_app_session") {
		t.Fatalf("application cookie was not namespaced: %q", applicationCookie.Name)
	}

	secondRequest := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8081/api", nil)
	secondRequest.Host = activationURL.Host
	secondRequest.AddCookie(accessCookie)
	secondRequest.AddCookie(applicationCookie)
	secondResponse := httptest.NewRecorder()
	service.ServeHTTP(secondResponse, secondRequest)
	secondUpstream := <-forwarder.requests
	if !strings.Contains(secondUpstream, "Cookie: app_session=abc") || strings.Contains(secondUpstream, applicationCookie.Name) {
		t.Fatalf("application cookie was not restored for upstream:\n%s", secondUpstream)
	}
}

func TestActiveSessionIsRemovedAfterAuthenticationRevocation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	authenticator := &testAuthenticator{allowed: true}
	service, err := NewService(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), authenticator,
		testAuthorizer{}, nil, &httpForwarder{requests: make(chan string, 1)}, Config{
			Enabled: true, ExternalURL: "http://127.0.0.1:8081", ActivationTTL: time.Minute,
			SessionTTL: time.Minute, RevalidateInterval: 5 * time.Millisecond, OperationTimeout: time.Second,
			MaxPending: 4, MaxActive: 4, MaxConnections: 4, MaxConnectionsPerSession: 2,
		})
	if err != nil {
		t.Fatal(err)
	}
	ticket, err := service.Create(validCreateInput(time.Now().UTC()))
	if err != nil {
		t.Fatal(err)
	}
	activationURL, _ := url.Parse(ticket.AccessURL)
	activationRequest := httptest.NewRequest(http.MethodGet, ticket.AccessURL, nil)
	activationRequest.Host = activationURL.Host
	activationResponse := httptest.NewRecorder()
	service.ServeHTTP(activationResponse, activationRequest)
	accessCookie := responseCookie(t, activationResponse.Result(), accessCookieName)
	authenticator.revoke()

	deadline := time.Now().Add(time.Second)
	for {
		request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8081/", nil)
		request.Host = activationURL.Host
		request.AddCookie(accessCookie)
		response := httptest.NewRecorder()
		service.ServeHTTP(response, request)
		if response.Code == http.StatusUnauthorized {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("active session remained available after authentication revocation")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestPodAccessProxiesWebSocketUpgrade(t *testing.T) {
	t.Parallel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	externalURL := "http://" + listener.Addr().String()
	service, err := NewService(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)),
		&testAuthenticator{allowed: true}, testAuthorizer{}, nil, webSocketForwarder{}, Config{
			Enabled: true, ExternalURL: externalURL, ActivationTTL: time.Minute,
			SessionTTL: time.Minute, RevalidateInterval: time.Minute, OperationTimeout: time.Second,
			MaxPending: 4, MaxActive: 4, MaxConnections: 4, MaxConnectionsPerSession: 2,
		})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := &http.Server{Handler: service, ReadHeaderTimeout: time.Second}
	serveDone := make(chan error, 1)
	go func() { serveDone <- httpServer.Serve(listener) }()
	defer func() {
		shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), time.Second)
		defer shutdownCancel()
		_ = httpServer.Shutdown(shutdownContext)
		<-serveDone
	}()

	ticket, err := service.Create(validCreateInput(time.Now().UTC()))
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	activationResponse, err := client.Get(ticket.AccessURL)
	if err != nil {
		t.Fatal(err)
	}
	_ = activationResponse.Body.Close()
	accessCookie := responseCookie(t, activationResponse, accessCookieName)
	header := http.Header{}
	header.Add("Cookie", accessCookie.String())
	connection, response, err := websocket.DefaultDialer.Dial("ws://"+listener.Addr().String()+"/live", header)
	if err != nil {
		if response != nil {
			_ = response.Body.Close()
		}
		t.Fatal(err)
	}
	defer connection.Close()
	if err := connection.WriteMessage(websocket.TextMessage, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	messageType, message, err := connection.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if messageType != websocket.TextMessage || string(message) != "hello" {
		t.Fatalf("WebSocket echo = type %d %q", messageType, message)
	}
}

func TestCreateIsIdempotentAndRejectsTargetReuse(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service, err := NewService(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)),
		&testAuthenticator{allowed: true}, testAuthorizer{}, nil,
		&httpForwarder{requests: make(chan string, 1)}, Config{Enabled: true, ExternalURL: "http://127.0.0.1:8081"})
	if err != nil {
		t.Fatal(err)
	}
	input := validCreateInput(time.Now().UTC())
	first, err := service.Create(input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Create(input)
	if err != nil || second.AccessURL != first.AccessURL {
		t.Fatalf("idempotent create = %+v, %v; want same ticket", second, err)
	}
	input.Port++
	if _, err := service.Create(input); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("target reuse error = %v, want idempotency conflict", err)
	}
}

func validCreateInput(now time.Time) CreateInput {
	return CreateInput{
		UserID: testUserID, AuthSessionID: testAuthID, AuthSessionToken: testSessionToken,
		IdempotencyKey: "pod-access-test-1", ClusterID: testClusterID, Namespace: "default",
		PodName: "api-0", PodUID: "pod-uid", RequestID: testRequestID, Port: 8080, Confirm: true, Now: now,
	}
}

func responseCookie(t *testing.T, response *http.Response, name string) *http.Cookie {
	t.Helper()
	for _, cookie := range response.Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	t.Fatalf("response cookie %q not found", name)
	return nil
}

func responseCookieWithPrefix(t *testing.T, response *http.Response, prefix string) *http.Cookie {
	t.Helper()
	for _, cookie := range response.Cookies() {
		if strings.HasPrefix(cookie.Name, prefix) {
			return cookie
		}
	}
	t.Fatalf("response cookie prefix %q not found", prefix)
	return nil
}

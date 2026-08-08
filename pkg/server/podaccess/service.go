package podaccess

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/togettoyou/zke/pkg/server/audit"
	"github.com/togettoyou/zke/pkg/server/auditaction"
	"github.com/togettoyou/zke/pkg/server/auth"
	"github.com/togettoyou/zke/pkg/server/podportforward"
	"github.com/togettoyou/zke/pkg/server/rbac"
	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
	"github.com/togettoyou/zke/pkg/shared/identifier"
	"github.com/togettoyou/zke/pkg/shared/validation"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
)

const (
	activationPathPrefix   = "/.zke-pod-access/open/"
	accessCookieName       = "zke_pod_access"
	opaqueTokenBytes       = 32
	proxyBufferBytes       = 32 * 1024
	maxResponseHeaderBytes = 1024 * 1024
)

var (
	ErrInvalidInput        = errors.New("invalid Pod access input")
	ErrDisabled            = errors.New("Pod access is disabled")
	ErrCapacity            = errors.New("Pod access capacity is exhausted")
	ErrIdempotencyConflict = errors.New("Pod access idempotency conflict")
	ErrActivationNotFound  = errors.New("Pod access activation was not found")
	ErrActivationExpired   = errors.New("Pod access activation expired")
	ErrAccessNotFound      = errors.New("Pod access session was not found")
	ErrAccessExpired       = errors.New("Pod access session expired")
	ErrAccessRevoked       = errors.New("Pod access permission was revoked")
	ErrByteLimit           = errors.New("Pod access byte limit reached")
)

type Authenticator interface {
	Authenticate(context.Context, string, time.Time) (auth.Identity, error)
}

type Authorizer interface {
	AuthorizeCluster(context.Context, string, rbac.Permission, string) (rbac.ResolvedScope, error)
}

type Auditor interface {
	RecordClusterEvent(context.Context, audit.ClusterEventInput) error
}

type Forwarder interface {
	Run(context.Context, podportforward.Session, agentprotocol.PodPortForwardPeer) (podportforward.Result, error)
}

type Config struct {
	Enabled                  bool
	ExternalURL              string
	ActivationTTL            time.Duration
	SessionTTL               time.Duration
	RevalidateInterval       time.Duration
	OperationTimeout         time.Duration
	IdleConnectionTimeout    time.Duration
	MaxPending               int
	MaxActive                int
	MaxConnections           int
	MaxConnectionsPerSession int
	MaxClientBytes           uint64
	MaxPodBytes              uint64
}

type CreateInput struct {
	UserID, AuthSessionID, AuthSessionToken, IdempotencyKey string
	ClusterID, Namespace, PodName, PodUID                   string
	RequestID                                               string
	Port                                                    uint32
	Confirm                                                 bool
	Now                                                     time.Time
}

type Ticket struct {
	AccessURL  string
	ExpiresAt  time.Time
	SessionTTL time.Duration
}

type pendingSession struct {
	CreateInput
	expiresAt time.Time
}

type idempotencyRecord struct {
	fingerprint [sha256.Size]byte
	ticket      Ticket
}

type activeSession struct {
	service      *Service
	key          [sha256.Size]byte
	input        CreateInput
	forward      podportforward.Session
	expiresAt    time.Time
	cookiePrefix string
	ctx          context.Context
	cancel       context.CancelFunc
	connections  chan struct{}
	transport    *http.Transport
	proxy        *httputil.ReverseProxy
	clientBytes  atomic.Uint64
	podBytes     atomic.Uint64
	closeOnce    sync.Once
	failureOnce  sync.Once
}

type Service struct {
	rootContext context.Context
	logger      *slog.Logger
	auth        Authenticator
	authorizer  Authorizer
	auditor     Auditor
	forwarder   Forwarder
	config      Config
	externalURL *url.URL
	secure      bool
	connections chan struct{}

	mutex       sync.Mutex
	pending     map[[sha256.Size]byte]pendingSession
	active      map[[sha256.Size]byte]*activeSession
	idempotency map[string]idempotencyRecord
}

func NewService(ctx context.Context, logger *slog.Logger, authenticator Authenticator,
	authorizer Authorizer, auditor Auditor, forwarder Forwarder, config Config) (*Service, error) {
	if !config.Enabled {
		return nil, ErrDisabled
	}
	if ctx == nil || logger == nil || authenticator == nil || authorizer == nil || forwarder == nil {
		return nil, ErrInvalidInput
	}
	externalURL, err := url.Parse(config.ExternalURL)
	if err != nil || externalURL.Host == "" {
		return nil, ErrInvalidInput
	}
	applyDefaults(&config)
	return &Service{
		rootContext: ctx,
		logger:      logger,
		auth:        authenticator,
		authorizer:  authorizer,
		auditor:     auditor,
		forwarder:   forwarder,
		config:      config,
		externalURL: externalURL,
		secure:      externalURL.Scheme == "https",
		connections: make(chan struct{}, config.MaxConnections),
		pending:     make(map[[sha256.Size]byte]pendingSession),
		active:      make(map[[sha256.Size]byte]*activeSession),
		idempotency: make(map[string]idempotencyRecord),
	}, nil
}

func applyDefaults(config *Config) {
	if config.ActivationTTL <= 0 {
		config.ActivationTTL = 30 * time.Second
	}
	if config.SessionTTL <= 0 {
		config.SessionTTL = 15 * time.Minute
	}
	if config.RevalidateInterval <= 0 {
		config.RevalidateInterval = 15 * time.Second
	}
	if config.OperationTimeout <= 0 {
		config.OperationTimeout = 10 * time.Second
	}
	if config.IdleConnectionTimeout <= 0 {
		config.IdleConnectionTimeout = 60 * time.Second
	}
	if config.MaxPending <= 0 {
		config.MaxPending = 1024
	}
	if config.MaxActive <= 0 {
		config.MaxActive = 256
	}
	if config.MaxConnections <= 0 {
		config.MaxConnections = 128
	}
	if config.MaxConnectionsPerSession <= 0 {
		config.MaxConnectionsPerSession = 2
	}
	if config.MaxClientBytes == 0 {
		config.MaxClientBytes = agentprotocol.DefaultMaxPodPortForwardClientBytes
	}
	if config.MaxPodBytes == 0 {
		config.MaxPodBytes = agentprotocol.DefaultMaxPodPortForwardPodBytes
	}
}

func (service *Service) Create(input CreateInput) (Ticket, error) {
	if service == nil || validateCreateInput(input) != nil {
		return Ticket{}, ErrInvalidInput
	}
	if !input.Confirm {
		return Ticket{}, podportforward.ErrConfirmationRequired
	}
	fingerprint := sha256.Sum256([]byte(strings.Join([]string{
		input.AuthSessionID, input.ClusterID, input.Namespace, input.PodName, input.PodUID,
		strconv.FormatUint(uint64(input.Port), 10),
	}, "\x00")))
	idempotencyKey := input.UserID + "\x00" + input.IdempotencyKey
	service.mutex.Lock()
	defer service.mutex.Unlock()
	service.removeExpiredLocked(input.Now)
	if record, exists := service.idempotency[idempotencyKey]; exists {
		if record.fingerprint != fingerprint {
			return Ticket{}, ErrIdempotencyConflict
		}
		return record.ticket, nil
	}
	if len(service.pending) >= service.config.MaxPending || len(service.idempotency) >= service.config.MaxPending {
		return Ticket{}, ErrCapacity
	}
	token, digest, err := newOpaqueToken()
	if err != nil {
		return Ticket{}, fmt.Errorf("generate Pod access activation: %w", err)
	}
	expiresAt := input.Now.Add(service.config.ActivationTTL)
	service.pending[digest] = pendingSession{CreateInput: input, expiresAt: expiresAt}
	activation := *service.externalURL
	activation.Path = activationPathPrefix + token
	activation.RawPath = ""
	ticket := Ticket{AccessURL: activation.String(), ExpiresAt: expiresAt, SessionTTL: service.config.SessionTTL}
	service.idempotency[idempotencyKey] = idempotencyRecord{fingerprint: fingerprint, ticket: ticket}
	return ticket, nil
}

func validateCreateInput(input CreateInput) error {
	if !validation.IsUUID(input.UserID) || !validation.IsUUID(input.AuthSessionID) || input.AuthSessionToken == "" ||
		!validation.IsIdempotencyKey(input.IdempotencyKey) ||
		!validation.IsUUID(input.ClusterID) || !validation.IsUUID(input.RequestID) || input.Now.IsZero() ||
		len(k8svalidation.IsDNS1123Label(input.Namespace)) != 0 ||
		len(k8svalidation.IsDNS1123Subdomain(input.PodName)) != 0 || input.PodUID == "" || len(input.PodUID) > 256 ||
		strings.TrimSpace(input.PodUID) != input.PodUID || input.Port == 0 || input.Port > 65535 {
		return ErrInvalidInput
	}
	return nil
}

func (service *Service) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	setPrivateHeaders(writer.Header())
	if !sameExternalHost(request.Host, service.externalURL) {
		http.Error(writer, "Pod access host is invalid", http.StatusMisdirectedRequest)
		return
	}
	if strings.HasPrefix(request.URL.Path, activationPathPrefix) {
		service.serveActivation(writer, request)
		return
	}
	cookie, err := request.Cookie(accessCookieName)
	if err != nil || cookie.Value == "" {
		service.writeAccessRequired(writer, http.StatusUnauthorized)
		return
	}
	session, err := service.resolve(cookie.Value, time.Now().UTC())
	if err != nil {
		service.clearAccessCookie(writer)
		service.writeAccessRequired(writer, http.StatusUnauthorized)
		return
	}
	rewriteRequestCookies(request, session.cookiePrefix)
	session.proxy.ServeHTTP(writer, request)
}

func (service *Service) serveActivation(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodGet+", "+http.MethodPost)
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token := strings.TrimPrefix(request.URL.Path, activationPathPrefix)
	if !validOpaqueToken(token) {
		http.NotFound(writer, request)
		return
	}
	oldCookie, _ := request.Cookie(accessCookieName)
	if oldCookie != nil && oldCookie.Value != "" {
		if _, err := service.resolve(oldCookie.Value, time.Now().UTC()); err == nil {
			if request.Method == http.MethodGet {
				service.writeReplacementRequired(writer, request.URL.EscapedPath())
				return
			}
			service.deactivateToken(oldCookie.Value, false)
		} else {
			service.clearAccessCookie(writer)
		}
	}
	cookieToken, session, err := service.activate(request.Context(), token, time.Now().UTC())
	if err != nil {
		service.writeAccessRequired(writer, http.StatusGone)
		return
	}
	http.SetCookie(writer, &http.Cookie{
		Name: accessCookieName, Value: cookieToken, Path: "/", Expires: session.expiresAt,
		MaxAge: int(time.Until(session.expiresAt).Seconds()), HttpOnly: true, Secure: service.secure,
		SameSite: http.SameSiteLaxMode,
	})
	writer.Header().Set("Location", "/")
	writer.WriteHeader(http.StatusSeeOther)
}

func (service *Service) writeReplacementRequired(writer http.ResponseWriter, activationPath string) {
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.WriteHeader(http.StatusConflict)
	_, _ = io.WriteString(writer, "<!doctype html><meta charset=utf-8><title>Pod access already active</title>"+
		"<h1>当前浏览器已有 Pod 访问会话</h1>"+
		"<p>为避免旧标签页静默访问到新的 Pod，本次地址尚未激活。可改用隐私窗口，或先关闭旧页面并明确替换当前入口。</p>"+
		"<form method=post action=\""+html.EscapeString(activationPath)+"\"><button type=submit>结束旧入口并继续</button></form>")
}

func (service *Service) activate(ctx context.Context, token string, now time.Time) (string, *activeSession, error) {
	digest := digestToken(token)
	service.mutex.Lock()
	pending, exists := service.pending[digest]
	if exists {
		delete(service.pending, digest)
	}
	service.removeExpiredLocked(now)
	service.mutex.Unlock()
	if !exists {
		return "", nil, ErrActivationNotFound
	}
	if !now.Before(pending.expiresAt) {
		return "", nil, ErrActivationExpired
	}
	if err := service.revalidate(ctx, pending.CreateInput, now); err != nil {
		service.record(pending.CreateInput, "denied")
		return "", nil, err
	}
	cookieToken, key, err := newOpaqueToken()
	if err != nil {
		service.record(pending.CreateInput, "failed")
		return "", nil, err
	}
	forwardID, err := identifier.NewUUID()
	if err != nil {
		service.record(pending.CreateInput, "failed")
		return "", nil, err
	}
	prefixBytes := make([]byte, 9)
	if _, err := rand.Read(prefixBytes); err != nil {
		service.record(pending.CreateInput, "failed")
		return "", nil, err
	}
	sessionContext, cancel := context.WithDeadline(service.rootContext, now.Add(service.config.SessionTTL))
	session := &activeSession{
		service: service, key: key, input: pending.CreateInput,
		forward: podportforward.Session{ID: forwardID, UserID: pending.UserID, AuthSessionID: pending.AuthSessionID,
			ClusterID: pending.ClusterID, Namespace: pending.Namespace, PodName: pending.PodName,
			PodUID: pending.PodUID, Port: pending.Port, CreatedAt: now, ExpiresAt: now.Add(service.config.SessionTTL)},
		expiresAt:    now.Add(service.config.SessionTTL),
		cookiePrefix: "zke_pa_" + base64.RawURLEncoding.EncodeToString(prefixBytes) + "_",
		ctx:          sessionContext, cancel: cancel,
		connections: make(chan struct{}, service.config.MaxConnectionsPerSession),
	}
	session.transport = session.newTransport()
	session.proxy = session.newProxy()
	service.mutex.Lock()
	service.removeExpiredLocked(now)
	if len(service.active) >= service.config.MaxActive {
		service.mutex.Unlock()
		session.close(false)
		service.record(pending.CreateInput, "failed")
		return "", nil, ErrCapacity
	}
	service.active[key] = session
	service.mutex.Unlock()
	service.record(pending.CreateInput, "succeeded")
	go session.monitor()
	return cookieToken, session, nil
}

func (service *Service) resolve(token string, now time.Time) (*activeSession, error) {
	if !validOpaqueToken(token) {
		return nil, ErrAccessNotFound
	}
	key := digestToken(token)
	service.mutex.Lock()
	session, exists := service.active[key]
	service.mutex.Unlock()
	if !exists {
		return nil, ErrAccessNotFound
	}
	if !now.Before(session.expiresAt) || session.ctx.Err() != nil {
		service.deactivate(key, false)
		return nil, ErrAccessExpired
	}
	return session, nil
}

func (service *Service) revalidate(parent context.Context, input CreateInput, now time.Time) error {
	ctx, cancel := context.WithTimeout(parent, service.config.OperationTimeout)
	defer cancel()
	identity, err := service.auth.Authenticate(ctx, input.AuthSessionToken, now)
	if err != nil || identity.User.ID != input.UserID || identity.SessionID != input.AuthSessionID {
		return ErrAccessRevoked
	}
	if _, err := service.authorizer.AuthorizeCluster(ctx, input.UserID, rbac.PermissionClusterPodPortForward, input.ClusterID); err != nil {
		return ErrAccessRevoked
	}
	return nil
}

func (service *Service) deactivateToken(token string, denied bool) {
	service.deactivate(digestToken(token), denied)
}

func (service *Service) deactivate(key [sha256.Size]byte, denied bool) {
	service.mutex.Lock()
	session := service.active[key]
	delete(service.active, key)
	service.mutex.Unlock()
	if session != nil {
		session.close(denied)
	}
}

func (service *Service) removeExpiredLocked(now time.Time) {
	for key, pending := range service.pending {
		if !now.Before(pending.expiresAt) {
			delete(service.pending, key)
		}
	}
	for key, record := range service.idempotency {
		if !now.Before(record.ticket.ExpiresAt) {
			delete(service.idempotency, key)
		}
	}
	for key, session := range service.active {
		if !now.Before(session.expiresAt) || session.ctx.Err() != nil {
			delete(service.active, key)
			go session.close(false)
		}
	}
}

func (service *Service) record(input CreateInput, result string) {
	if service.auditor == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(service.rootContext), service.config.OperationTimeout)
	defer cancel()
	err := service.auditor.RecordClusterEvent(ctx, audit.ClusterEventInput{
		ActorUserID: input.UserID, ClusterID: input.ClusterID,
		Action: auditaction.KubernetesPodPortForward, TargetType: auditaction.TargetKubernetesResource,
		TargetName: fmt.Sprintf("core/v1/pods %s/%s uid:%s port-forward:%d", input.Namespace, input.PodName, input.PodUID, input.Port),
		Result:     result, RequestID: input.RequestID,
	})
	if err != nil {
		service.logger.Error("record Pod access audit", slog.String("request_id", input.RequestID), slog.String("error", err.Error()))
	}
}

func (service *Service) clearAccessCookie(writer http.ResponseWriter) {
	http.SetCookie(writer, &http.Cookie{Name: accessCookieName, Path: "/", Expires: time.Unix(1, 0).UTC(),
		MaxAge: -1, HttpOnly: true, Secure: service.secure, SameSite: http.SameSiteLaxMode})
}

func (service *Service) writeAccessRequired(writer http.ResponseWriter, status int) {
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.WriteHeader(status)
	_, _ = io.WriteString(writer, "<!doctype html><meta charset=utf-8><title>Pod access unavailable</title><p>Pod 访问地址无效、已过期或权限已被收回，请返回 ZKE 重新创建。</p>")
}

func setPrivateHeaders(header http.Header) {
	header.Set("Cache-Control", "no-store")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("X-Content-Type-Options", "nosniff")
}

func sameExternalHost(requestHost string, externalURL *url.URL) bool {
	if strings.EqualFold(requestHost, externalURL.Host) {
		return true
	}
	requestURL, err := url.Parse("//" + requestHost)
	if err != nil || requestURL.Hostname() == "" {
		return false
	}
	requestName, requestPort := requestURL.Hostname(), requestURL.Port()
	externalName, externalPort := externalURL.Hostname(), externalURL.Port()
	defaultPort := "80"
	if externalURL.Scheme == "https" {
		defaultPort = "443"
	}
	if requestPort == "" {
		requestPort = defaultPort
	}
	if externalPort == "" {
		externalPort = defaultPort
	}
	return strings.EqualFold(requestName, externalName) && requestPort == externalPort
}

func newOpaqueToken() (string, [sha256.Size]byte, error) {
	value := make([]byte, opaqueTokenBytes)
	if _, err := rand.Read(value); err != nil {
		return "", [sha256.Size]byte{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(value)
	return token, digestToken(token), nil
}

func validOpaqueToken(token string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	return err == nil && len(decoded) == opaqueTokenBytes
}

func digestToken(token string) [sha256.Size]byte {
	return sha256.Sum256([]byte(token))
}

func (session *activeSession) monitor() {
	ticker := time.NewTicker(session.service.config.RevalidateInterval)
	defer ticker.Stop()
	for {
		select {
		case <-session.ctx.Done():
			session.service.deactivate(session.key, false)
			return
		case now := <-ticker.C:
			if err := session.service.revalidate(session.ctx, session.input, now.UTC()); err != nil {
				session.service.deactivate(session.key, true)
				return
			}
		}
	}
}

func (session *activeSession) close(denied bool) {
	session.closeOnce.Do(func() {
		if denied {
			session.service.record(session.input, "denied")
		}
		session.cancel()
		if session.transport != nil {
			session.transport.CloseIdleConnections()
		}
	})
}

func (session *activeSession) fail() {
	session.failureOnce.Do(func() {
		session.service.record(session.input, "failed")
		session.service.deactivate(session.key, false)
	})
}

func (session *activeSession) newTransport() *http.Transport {
	return &http.Transport{
		Proxy:                  nil,
		DialContext:            session.dialContext,
		ForceAttemptHTTP2:      false,
		MaxIdleConns:           session.service.config.MaxConnectionsPerSession,
		MaxIdleConnsPerHost:    session.service.config.MaxConnectionsPerSession,
		IdleConnTimeout:        session.service.config.IdleConnectionTimeout,
		ExpectContinueTimeout:  time.Second,
		MaxResponseHeaderBytes: maxResponseHeaderBytes,
	}
}

func (session *activeSession) newProxy() *httputil.ReverseProxy {
	target := &url.URL{Scheme: "http", Host: net.JoinHostPort(session.input.PodName, strconv.Itoa(int(session.input.Port)))}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(request *httputil.ProxyRequest) {
			originalHost := request.In.Host
			request.SetURL(target)
			request.Out.Host = originalHost
			request.SetXForwarded()
		},
		Transport:     session.transport,
		FlushInterval: 100 * time.Millisecond,
		ModifyResponse: func(response *http.Response) error {
			sanitizeResponseHeaders(response.Header)
			rewriteResponseCookies(response, session.cookiePrefix)
			return nil
		},
		ErrorHandler: func(writer http.ResponseWriter, _ *http.Request, err error) {
			setPrivateHeaders(writer.Header())
			status := http.StatusBadGateway
			if errors.Is(err, ErrCapacity) {
				status = http.StatusServiceUnavailable
				writer.Header().Set("Retry-After", "1")
			}
			http.Error(writer, "Pod service is unavailable", status)
		},
	}
	return proxy
}

func (session *activeSession) dialContext(ctx context.Context, _, _ string) (net.Conn, error) {
	if err := session.acquire(ctx); err != nil {
		return nil, err
	}
	client, peerConnection := net.Pipe()
	managed := &managedConnection{Conn: client, release: session.release}
	peer := &pipePeer{connection: peerConnection}
	go func() {
		_, err := session.service.forwarder.Run(session.ctx, session.forward, peer)
		_ = peerConnection.Close()
		_ = managed.Close()
		if errors.Is(err, podportforward.ErrPodReplaced) || errors.Is(err, podportforward.ErrClusterAccessDenied) {
			session.fail()
		}
	}()
	return &limitedConnection{Conn: managed, session: session}, nil
}

func (session *activeSession) acquire(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := session.ctx.Err(); err != nil {
		return ErrAccessExpired
	}
	select {
	case session.connections <- struct{}{}:
	default:
		return ErrCapacity
	}
	select {
	case session.service.connections <- struct{}{}:
		return nil
	default:
		<-session.connections
		return ErrCapacity
	}
}

func (session *activeSession) release() {
	<-session.service.connections
	<-session.connections
}

type managedConnection struct {
	net.Conn
	release func()
	once    sync.Once
}

func (connection *managedConnection) Close() error {
	err := connection.Conn.Close()
	connection.once.Do(connection.release)
	return err
}

type limitedConnection struct {
	net.Conn
	session *activeSession
}

func (connection *limitedConnection) Write(data []byte) (int, error) {
	if exceedsLimit(&connection.session.clientBytes, uint64(len(data)), connection.session.service.config.MaxClientBytes) {
		connection.session.fail()
		return 0, ErrByteLimit
	}
	return connection.Conn.Write(data)
}

func (connection *limitedConnection) Read(data []byte) (int, error) {
	read, err := connection.Conn.Read(data)
	if read > 0 && exceedsLimit(&connection.session.podBytes, uint64(read), connection.session.service.config.MaxPodBytes) {
		connection.session.fail()
		return read, ErrByteLimit
	}
	return read, err
}

func exceedsLimit(counter *atomic.Uint64, amount, maximum uint64) bool {
	for {
		current := counter.Load()
		if amount > maximum-current {
			return true
		}
		if counter.CompareAndSwap(current, current+amount) {
			return false
		}
	}
}

type pipePeer struct {
	connection net.Conn
}

func (peer *pipePeer) Read(ctx context.Context) ([]byte, error) {
	if deadline, ok := ctx.Deadline(); ok {
		_ = peer.connection.SetReadDeadline(deadline)
	}
	buffer := make([]byte, proxyBufferBytes)
	read, err := peer.connection.Read(buffer)
	if read > 0 {
		return buffer[:read], nil
	}
	return nil, err
}

func (peer *pipePeer) Write(ctx context.Context, data []byte) error {
	if deadline, ok := ctx.Deadline(); ok {
		_ = peer.connection.SetWriteDeadline(deadline)
	}
	for len(data) > 0 {
		written, err := peer.connection.Write(data)
		if err != nil {
			return err
		}
		data = data[written:]
	}
	return nil
}

func rewriteRequestCookies(request *http.Request, prefix string) {
	cookies := request.Cookies()
	request.Header.Del("Cookie")
	for _, cookie := range cookies {
		if strings.HasPrefix(cookie.Name, prefix) {
			cookie.Name = strings.TrimPrefix(cookie.Name, prefix)
			request.AddCookie(cookie)
		}
	}
}

func rewriteResponseCookies(response *http.Response, prefix string) {
	cookies := response.Cookies()
	response.Header.Del("Set-Cookie")
	for _, cookie := range cookies {
		cookie.Name = prefix + cookie.Name
		cookie.Domain = ""
		response.Header.Add("Set-Cookie", cookie.String())
	}
}

func sanitizeResponseHeaders(header http.Header) {
	// These headers alter browser state outside the proxied document and, for
	// cookies/HSTS, outside the port-specific Origin. The deployment rather
	// than an untrusted Pod owns policy for the shared host.
	for _, name := range []string{
		"Alt-Svc",
		"Clear-Site-Data",
		"NEL",
		"Public-Key-Pins",
		"Public-Key-Pins-Report-Only",
		"Report-To",
		"Strict-Transport-Security",
	} {
		header.Del(name)
	}
}

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
	"github.com/togettoyou/zke/pkg/shared/requestctx"
	"github.com/togettoyou/zke/pkg/shared/validation"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
)

const (
	activationPathPrefix   = "/.zke-pod-access/open/"
	accessCookieName       = "zke_pod_access"
	opaqueTokenBytes       = 32
	proxyBufferBytes       = 32 * 1024
	maxResponseHeaderBytes = 1024 * 1024
	sessionTTL15Minutes    = 15 * time.Minute
	sessionTTL30Minutes    = 30 * time.Minute
	sessionTTL1Hour        = time.Hour
	podAccessPageStart     = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="light dark">
<style>
:root {
  color-scheme: light;
  --page: #f4f6fb;
  --glow: rgba(79, 70, 229, .16);
  --card: rgba(255, 255, 255, .92);
  --border: #e1e6f0;
  --text: #182033;
  --muted: #68738a;
  --soft: #f7f8fc;
  --primary: #5262e5;
  --primary-hover: #4453cf;
  --warning: #b86c0f;
  --warning-soft: #fff5dc;
  --danger: #c24432;
  --danger-soft: #fff0ed;
  --shadow: 0 24px 64px rgba(29, 39, 69, .13);
}
* { box-sizing: border-box; }
body {
  margin: 0;
  min-height: 100vh;
  min-height: 100dvh;
  display: grid;
  place-items: center;
  padding: 24px;
  color: var(--text);
  background:
    radial-gradient(circle at 15% 12%, var(--glow), transparent 28rem),
    radial-gradient(circle at 88% 86%, rgba(43, 184, 196, .09), transparent 26rem),
    var(--page);
  font: 15px/1.65 -apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC", "Microsoft YaHei", sans-serif;
}
.status-card {
  width: min(100%, 640px);
  overflow: hidden;
  padding: 38px;
  border: 1px solid var(--border);
  border-radius: 24px;
  background: var(--card);
  box-shadow: var(--shadow);
  backdrop-filter: blur(16px);
}
.brand { display: flex; align-items: center; gap: 10px; margin-bottom: 28px; color: var(--muted); font-weight: 650; }
.brand-mark { display: grid; place-items: center; width: 30px; height: 30px; border-radius: 9px; color: white; background: linear-gradient(145deg, #6978ef, #4351ce); }
.brand-mark svg { width: 18px; height: 18px; }
.status-icon { display: grid; place-items: center; width: 58px; height: 58px; margin-bottom: 20px; border-radius: 17px; }
.status-icon svg { width: 30px; height: 30px; }
.status-icon.warning { color: var(--warning); background: var(--warning-soft); }
.status-icon.danger { color: var(--danger); background: var(--danger-soft); }
h1 { margin: 0 0 10px; font-size: clamp(25px, 4vw, 32px); line-height: 1.25; letter-spacing: -.02em; }
.lead { margin: 0; color: var(--muted); font-size: 16px; }
.notice { display: grid; grid-template-columns: auto 1fr; gap: 12px; margin: 24px 0; padding: 16px 18px; border: 1px solid var(--border); border-radius: 14px; background: var(--soft); }
.notice svg { width: 19px; height: 19px; margin-top: 3px; color: var(--primary); }
.notice strong { display: block; margin-bottom: 2px; }
.notice p { margin: 0; color: var(--muted); }
.actions { display: flex; align-items: center; gap: 16px; margin-top: 26px; }
.primary-button {
  appearance: none;
  border: 0;
  border-radius: 11px;
  padding: 12px 20px;
  color: white;
  background: var(--primary);
  box-shadow: 0 8px 18px rgba(82, 98, 229, .24);
  font: inherit;
  font-weight: 650;
  cursor: pointer;
  transition: transform .15s ease, background .15s ease, box-shadow .15s ease;
}
.primary-button:hover { transform: translateY(-1px); background: var(--primary-hover); box-shadow: 0 10px 22px rgba(82, 98, 229, .3); }
.primary-button:focus-visible { outline: 3px solid rgba(82, 98, 229, .28); outline-offset: 3px; }
.alternative { color: var(--muted); font-size: 14px; }
.footnote { margin: 24px 0 0; padding-top: 20px; border-top: 1px solid var(--border); color: var(--muted); font-size: 13px; }
@media (max-width: 560px) {
  body { padding: 14px; }
  .status-card { padding: 26px 22px; border-radius: 19px; }
  .brand { margin-bottom: 22px; }
  .actions { align-items: stretch; flex-direction: column; }
  .primary-button { width: 100%; }
}
@media (prefers-color-scheme: dark) {
  :root {
    color-scheme: dark;
    --page: #101522;
    --glow: rgba(91, 104, 230, .2);
    --card: rgba(25, 32, 49, .94);
    --border: #303a51;
    --text: #edf1fa;
    --muted: #aab3c6;
    --soft: #20283a;
    --primary: #7180f0;
    --primary-hover: #8190fa;
    --warning: #f1b75d;
    --warning-soft: rgba(184, 108, 15, .17);
    --danger: #f08b7b;
    --danger-soft: rgba(194, 68, 50, .17);
    --shadow: 0 24px 70px rgba(0, 0, 0, .3);
  }
}
</style>`
	podAccessBrand = `<div class="brand"><span class="brand-mark" aria-hidden="true"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8"><path d="M12 3 5 7v10l7 4 7-4V7l-7-4Z"/><path d="m5 7 7 4 7-4M12 11v10"/></svg></span><span>ZKE Pod Access</span></div>`
)

var (
	ErrInvalidInput         = errors.New("invalid Pod access input")
	ErrDisabled             = errors.New("Pod access is disabled")
	ErrCapacity             = errors.New("Pod access capacity is exhausted")
	ErrConfirmationRequired = errors.New("Pod access confirmation is required")
	ErrIdempotencyConflict  = errors.New("Pod access idempotency conflict")
	ErrTargetReserved       = errors.New("Pod already has a Pod access session or activation")
	ErrActivationNotFound   = errors.New("Pod access activation was not found")
	ErrActivationExpired    = errors.New("Pod access activation expired")
	ErrAccessNotFound       = errors.New("Pod access session was not found")
	ErrAccessExpired        = errors.New("Pod access session expired")
	ErrAccessRevoked        = errors.New("Pod access permission was revoked")
	ErrAccessReplaced       = errors.New("Pod access session was replaced")
	ErrAccessFailed         = errors.New("Pod access upstream connection failed")
	ErrByteLimit            = errors.New("Pod access byte limit reached")
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
	Run(context.Context, podportforward.Session, agentprotocol.PodPortForwardPeer,
		uint64, uint64) (podportforward.Result, error)
}

type Config struct {
	Enabled                  bool
	ExternalURL              string
	ActivationTTL            time.Duration
	MaxSessionTTL            time.Duration
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
	SessionTTL                                              time.Duration
	ReplaceExisting                                         bool
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

type podTarget struct {
	clusterID string
	podUID    string
}

type targetReservation struct {
	key            [sha256.Size]byte
	idempotencyKey string
	active         bool
}

type endReason uint8

const (
	endExpired endReason = iota + 1
	endRevoked
	endReplaced
	endByteLimit
	endFailed
	endServerShutdown
)

type endedSession struct {
	reason    endReason
	endedAt   time.Time
	expiresAt time.Time
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

	mutex        sync.Mutex
	pending      map[[sha256.Size]byte]pendingSession
	active       map[[sha256.Size]byte]*activeSession
	reservations map[podTarget]targetReservation
	ended        map[[sha256.Size]byte]endedSession
	idempotency  map[string]idempotencyRecord
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
		rootContext:  ctx,
		logger:       logger,
		auth:         authenticator,
		authorizer:   authorizer,
		auditor:      auditor,
		forwarder:    forwarder,
		config:       config,
		externalURL:  externalURL,
		secure:       externalURL.Scheme == "https",
		connections:  make(chan struct{}, config.MaxConnections),
		pending:      make(map[[sha256.Size]byte]pendingSession),
		active:       make(map[[sha256.Size]byte]*activeSession),
		reservations: make(map[podTarget]targetReservation),
		ended:        make(map[[sha256.Size]byte]endedSession),
		idempotency:  make(map[string]idempotencyRecord),
	}, nil
}

func applyDefaults(config *Config) {
	if config.ActivationTTL <= 0 {
		config.ActivationTTL = 30 * time.Second
	}
	if config.MaxSessionTTL <= 0 {
		config.MaxSessionTTL = sessionTTL1Hour
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
		config.MaxConnectionsPerSession = 8
	}
	if config.MaxClientBytes == 0 {
		config.MaxClientBytes = agentprotocol.MaxPodPortForwardBytes
	}
	if config.MaxPodBytes == 0 {
		config.MaxPodBytes = agentprotocol.MaxPodPortForwardBytes
	}
}

func (service *Service) Create(input CreateInput) (Ticket, error) {
	if service == nil || validateCreateInput(input) != nil || input.SessionTTL > service.config.MaxSessionTTL {
		return Ticket{}, ErrInvalidInput
	}
	if !input.Confirm {
		return Ticket{}, ErrConfirmationRequired
	}
	fingerprint := sha256.Sum256([]byte(strings.Join([]string{
		input.AuthSessionID, input.ClusterID, input.Namespace, input.PodName, input.PodUID,
		strconv.FormatUint(uint64(input.Port), 10), strconv.FormatInt(int64(input.SessionTTL/time.Second), 10),
		strconv.FormatBool(input.ReplaceExisting),
	}, "\x00")))
	idempotencyKey := input.UserID + "\x00" + input.IdempotencyKey
	token, digest, err := newOpaqueToken()
	if err != nil {
		return Ticket{}, fmt.Errorf("generate Pod access activation: %w", err)
	}
	expiresAt := input.Now.Add(service.config.ActivationTTL)
	activation := *service.externalURL
	activation.Path = activationPathPrefix + token
	activation.RawPath = ""
	ticket := Ticket{AccessURL: activation.String(), ExpiresAt: expiresAt, SessionTTL: input.SessionTTL}
	target := input.target()
	var replaced *activeSession
	service.mutex.Lock()
	service.removeExpiredLocked(input.Now)
	if record, exists := service.idempotency[idempotencyKey]; exists {
		if record.fingerprint != fingerprint {
			service.mutex.Unlock()
			return Ticket{}, ErrIdempotencyConflict
		}
		service.mutex.Unlock()
		return record.ticket, nil
	}
	reservation, reserved := service.reservations[target]
	pendingCount, idempotencyCount := len(service.pending), len(service.idempotency)
	if reserved {
		if !input.ReplaceExisting {
			service.mutex.Unlock()
			return Ticket{}, ErrTargetReserved
		}
		if !reservation.active {
			if _, exists := service.pending[reservation.key]; exists {
				pendingCount--
			}
		}
		if _, exists := service.idempotency[reservation.idempotencyKey]; exists {
			idempotencyCount--
		}
	}
	// A replacement must not revoke the working session before the new ticket
	// has passed the same bounded-capacity check as a normal creation.
	if pendingCount >= service.config.MaxPending || idempotencyCount >= service.config.MaxPending {
		service.mutex.Unlock()
		return Ticket{}, ErrCapacity
	}
	if reserved {
		replaced = service.removeReservationLocked(target, reservation, input.Now, endReplaced)
	}
	service.pending[digest] = pendingSession{CreateInput: input, expiresAt: expiresAt}
	service.reservations[target] = targetReservation{key: digest, idempotencyKey: idempotencyKey}
	service.idempotency[idempotencyKey] = idempotencyRecord{fingerprint: fingerprint, ticket: ticket}
	service.mutex.Unlock()
	if replaced != nil {
		replaced.close()
		service.logSessionEnded(replaced, endReplaced)
		service.recordWithReason(replaced.input, "succeeded", "replaced")
	}
	service.logActivationCreated(input, expiresAt)
	return ticket, nil
}

func (input CreateInput) target() podTarget {
	return podTarget{clusterID: input.ClusterID, podUID: input.PodUID}
}

func validateCreateInput(input CreateInput) error {
	if !validation.IsUUID(input.UserID) || !validation.IsUUID(input.AuthSessionID) || input.AuthSessionToken == "" ||
		!validation.IsIdempotencyKey(input.IdempotencyKey) ||
		!validation.IsUUID(input.ClusterID) || !validation.IsUUID(input.RequestID) || input.Now.IsZero() ||
		len(k8svalidation.IsDNS1123Label(input.Namespace)) != 0 ||
		len(k8svalidation.IsDNS1123Subdomain(input.PodName)) != 0 || input.PodUID == "" || len(input.PodUID) > 256 ||
		strings.TrimSpace(input.PodUID) != input.PodUID || input.Port == 0 || input.Port > 65535 ||
		!supportedSessionTTL(input.SessionTTL) {
		return ErrInvalidInput
	}
	return nil
}

func supportedSessionTTL(value time.Duration) bool {
	return value == sessionTTL15Minutes || value == sessionTTL30Minutes || value == sessionTTL1Hour
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
		service.writeAccessRequired(writer, http.StatusUnauthorized, ErrAccessNotFound)
		return
	}
	session, err := service.resolve(cookie.Value, time.Now().UTC())
	if err != nil {
		service.clearAccessCookie(writer)
		service.writeAccessRequired(writer, accessErrorStatus(err), err)
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
			service.deactivateToken(oldCookie.Value, endReplaced)
		} else {
			service.clearAccessCookie(writer)
		}
	}
	cookieToken, session, err := service.activate(request.Context(), token, time.Now().UTC())
	if err != nil {
		service.writeAccessRequired(writer, http.StatusGone, err)
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
	setPodAccessPageHeaders(writer)
	writer.WriteHeader(http.StatusConflict)
	_, _ = io.WriteString(writer, podAccessPageStart+`<title>当前已有 Pod 访问会话 · ZKE</title>
</head>
<body>
<main class="status-card">`+podAccessBrand+`
  <div class="status-icon warning" aria-hidden="true"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M12 8v5m0 3h.01"/><path d="M10.3 3.9 2.6 17.1A2 2 0 0 0 4.3 20h15.4a2 2 0 0 0 1.7-2.9L13.7 3.9a2 2 0 0 0-3.4 0Z"/></svg></div>
  <h1>当前已有 Pod 访问会话</h1>
  <p class="lead">为避免旧标签页在不知情的情况下访问到新的 Pod，本次地址尚未激活。</p>
  <div class="notice"><svg aria-hidden="true" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="9"/><path d="M12 11v5m0-8h.01"/></svg><div><strong>请选择如何继续</strong><p>结束当前浏览器中的旧入口，或在隐私窗口中打开此地址以保留两个独立会话。</p></div></div>
  <div class="actions"><form method="post" action="`+html.EscapeString(activationPath)+`"><button class="primary-button" type="submit">结束旧入口并继续</button></form><span class="alternative">不会自动替换现有会话</span></div>
  <p class="footnote">Pod 访问会话与当前浏览器隔离，并会在到期或权限被收回后自动失效。</p>
</main>
</body>
</html>`)
}

func (service *Service) activate(ctx context.Context, token string, now time.Time) (string, *activeSession, error) {
	digest := digestToken(token)
	service.mutex.Lock()
	service.removeExpiredLocked(now)
	pending, exists := service.pending[digest]
	if exists {
		delete(service.pending, digest)
	}
	service.mutex.Unlock()
	if !exists {
		return "", nil, ErrActivationNotFound
	}
	if !now.Before(pending.expiresAt) {
		service.releasePendingReservation(pending.CreateInput.target(), digest)
		return "", nil, ErrActivationExpired
	}
	if err := service.revalidate(ctx, pending.CreateInput, now); err != nil {
		service.releasePendingReservation(pending.CreateInput.target(), digest)
		service.record(pending.CreateInput, "denied")
		return "", nil, err
	}
	cookieToken, key, err := newOpaqueToken()
	if err != nil {
		service.releasePendingReservation(pending.CreateInput.target(), digest)
		service.record(pending.CreateInput, "failed")
		return "", nil, err
	}
	forwardID, err := identifier.NewUUID()
	if err != nil {
		service.releasePendingReservation(pending.CreateInput.target(), digest)
		service.record(pending.CreateInput, "failed")
		return "", nil, err
	}
	prefixBytes := make([]byte, 9)
	if _, err := rand.Read(prefixBytes); err != nil {
		service.releasePendingReservation(pending.CreateInput.target(), digest)
		service.record(pending.CreateInput, "failed")
		return "", nil, err
	}
	sessionContext, cancel := context.WithDeadline(
		requestctx.WithID(service.rootContext, pending.RequestID),
		now.Add(pending.SessionTTL),
	)
	session := &activeSession{
		service: service, key: key, input: pending.CreateInput,
		forward: podportforward.Session{ID: forwardID, UserID: pending.UserID, AuthSessionID: pending.AuthSessionID,
			ClusterID: pending.ClusterID, Namespace: pending.Namespace, PodName: pending.PodName,
			PodUID: pending.PodUID, Port: pending.Port, CreatedAt: now, ExpiresAt: now.Add(pending.SessionTTL)},
		expiresAt:    now.Add(pending.SessionTTL),
		cookiePrefix: "zke_pa_" + base64.RawURLEncoding.EncodeToString(prefixBytes) + "_",
		ctx:          sessionContext, cancel: cancel,
		connections: make(chan struct{}, service.config.MaxConnectionsPerSession),
	}
	session.transport = session.newTransport()
	session.proxy = session.newProxy()
	service.mutex.Lock()
	service.removeExpiredLocked(now)
	reservation, reserved := service.reservations[pending.CreateInput.target()]
	if !reserved || reservation.active || reservation.key != digest {
		service.mutex.Unlock()
		session.close()
		service.record(pending.CreateInput, "failed")
		return "", nil, ErrActivationNotFound
	}
	if len(service.active) >= service.config.MaxActive {
		delete(service.reservations, pending.CreateInput.target())
		service.mutex.Unlock()
		session.close()
		service.record(pending.CreateInput, "failed")
		return "", nil, ErrCapacity
	}
	service.active[key] = session
	service.reservations[pending.CreateInput.target()] = targetReservation{
		key: key, idempotencyKey: reservation.idempotencyKey, active: true,
	}
	service.mutex.Unlock()
	service.record(pending.CreateInput, "succeeded")
	service.logSessionActivated(session)
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
	ended := service.ended[key]
	service.mutex.Unlock()
	if !exists {
		if ended.reason != 0 && now.Before(ended.expiresAt) {
			return nil, ended.reason.err()
		}
		return nil, ErrAccessNotFound
	}
	if !now.Before(session.expiresAt) || session.ctx.Err() != nil {
		service.deactivate(key, endExpired)
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

func (service *Service) deactivateToken(token string, reason endReason) {
	service.deactivate(digestToken(token), reason)
}

func (service *Service) deactivate(key [sha256.Size]byte, reason endReason) {
	service.mutex.Lock()
	session := service.active[key]
	delete(service.active, key)
	if session != nil {
		target := session.input.target()
		if reservation := service.reservations[target]; reservation.active && reservation.key == key {
			delete(service.reservations, target)
		}
		service.rememberEndedLocked(key, reason, time.Now().UTC(), session.expiresAt)
	}
	service.mutex.Unlock()
	if session != nil {
		session.close()
		service.logSessionEnded(session, reason)
		switch reason {
		case endRevoked:
			service.recordWithReason(session.input, "denied", "permission_revoked")
		case endReplaced:
			service.recordWithReason(session.input, "succeeded", "replaced")
		case endByteLimit:
			service.recordWithReason(session.input, "failed", "byte_limit")
		case endFailed:
			service.recordWithReason(session.input, "failed", "upstream_failure")
		}
	}
}

func (service *Service) releasePendingReservation(target podTarget, key [sha256.Size]byte) {
	service.mutex.Lock()
	if reservation := service.reservations[target]; !reservation.active && reservation.key == key {
		delete(service.reservations, target)
		delete(service.idempotency, reservation.idempotencyKey)
	}
	service.mutex.Unlock()
}

func (service *Service) removeReservationLocked(target podTarget, reservation targetReservation,
	now time.Time, reason endReason) *activeSession {
	delete(service.reservations, target)
	delete(service.idempotency, reservation.idempotencyKey)
	if reservation.active {
		session := service.active[reservation.key]
		delete(service.active, reservation.key)
		if session != nil {
			service.rememberEndedLocked(reservation.key, reason, now, session.expiresAt)
		}
		return session
	}
	delete(service.pending, reservation.key)
	return nil
}

func (service *Service) rememberEndedLocked(key [sha256.Size]byte, reason endReason, now, sessionExpiry time.Time) {
	expiresAt := sessionExpiry
	if maximum := now.Add(service.config.MaxSessionTTL); maximum.Before(expiresAt) {
		expiresAt = maximum
	}
	service.ended[key] = endedSession{reason: reason, endedAt: now, expiresAt: expiresAt}
	if len(service.ended) <= service.config.MaxActive {
		return
	}
	var oldestKey [sha256.Size]byte
	var oldest time.Time
	for candidate, ended := range service.ended {
		if oldest.IsZero() || ended.endedAt.Before(oldest) {
			oldestKey, oldest = candidate, ended.endedAt
		}
	}
	delete(service.ended, oldestKey)
}

func (reason endReason) err() error {
	switch reason {
	case endReplaced:
		return ErrAccessReplaced
	case endByteLimit:
		return ErrByteLimit
	case endRevoked:
		return ErrAccessRevoked
	case endFailed:
		return ErrAccessFailed
	default:
		return ErrAccessExpired
	}
}

func (service *Service) removeExpiredLocked(now time.Time) {
	for key, pending := range service.pending {
		if !now.Before(pending.expiresAt) {
			delete(service.pending, key)
			target := pending.CreateInput.target()
			if reservation := service.reservations[target]; !reservation.active && reservation.key == key {
				delete(service.reservations, target)
			}
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
			target := session.input.target()
			if reservation := service.reservations[target]; reservation.active && reservation.key == key {
				delete(service.reservations, target)
			}
			service.rememberEndedLocked(key, endExpired, now, session.expiresAt)
			go func(expired *activeSession) {
				expired.close()
				service.logSessionEnded(expired, endExpired)
			}(session)
		}
	}
	for key, ended := range service.ended {
		if !now.Before(ended.expiresAt) {
			delete(service.ended, key)
		}
	}
}

func (service *Service) record(input CreateInput, result string) {
	service.recordWithReason(input, result, "")
}

func (service *Service) recordWithReason(input CreateInput, result, reason string) {
	if service.auditor == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(service.rootContext), service.config.OperationTimeout)
	defer cancel()
	err := service.auditor.RecordClusterEvent(ctx, audit.ClusterEventInput{
		ActorUserID: input.UserID, ClusterID: input.ClusterID,
		Action: auditaction.KubernetesPodAccess, TargetType: auditaction.TargetKubernetesResource,
		TargetName: fmt.Sprintf("core/v1/pods %s/%s uid:%s pod-access:%d duration:%s%s",
			input.Namespace, input.PodName, input.PodUID, input.Port, input.SessionTTL, auditReason(reason)),
		Result: result, RequestID: input.RequestID,
	})
	if err != nil {
		service.logger.Error("record Pod access audit", slog.String("request_id", input.RequestID), slog.String("error", err.Error()))
	}
}

func auditReason(reason string) string {
	if reason == "" {
		return ""
	}
	return " end:" + reason
}

func (service *Service) logActivationCreated(input CreateInput, expiresAt time.Time) {
	service.logger.Info("Pod access activation created", append(
		podAccessLogAttributes(input),
		slog.Time("activation_expires_at", expiresAt),
		slog.Duration("session_duration", input.SessionTTL),
		slog.Bool("replace_existing", input.ReplaceExisting),
	)...)
}

func (service *Service) logSessionActivated(session *activeSession) {
	service.logger.Info("Pod access session activated", append(
		podAccessSessionLogAttributes(session),
		slog.Time("session_expires_at", session.expiresAt),
	)...)
}

func (service *Service) logSessionEnded(session *activeSession, reason endReason) {
	if session == nil {
		return
	}
	attributes := append(
		podAccessSessionLogAttributes(session),
		slog.String("reason", reason.String()),
		slog.Duration("duration", time.Since(session.forward.CreatedAt)),
		slog.Uint64("client_bytes", session.clientBytes.Load()),
		slog.Uint64("pod_bytes", session.podBytes.Load()),
	)
	if reason == endRevoked || reason == endByteLimit || reason == endFailed {
		service.logger.Warn("Pod access session closed", attributes...)
		return
	}
	service.logger.Info("Pod access session closed", attributes...)
}

func podAccessLogAttributes(input CreateInput) []any {
	return []any{
		slog.String("request_id", input.RequestID),
		slog.String("user_id", input.UserID),
		slog.String("cluster_id", input.ClusterID),
		slog.String("namespace", input.Namespace),
		slog.String("pod_name", input.PodName),
		slog.String("pod_uid", input.PodUID),
		slog.Int("pod_port", int(input.Port)),
	}
}

func podAccessSessionLogAttributes(session *activeSession) []any {
	return append(
		podAccessLogAttributes(session.input),
		slog.String("session_id", session.forward.ID),
	)
}

func (reason endReason) String() string {
	switch reason {
	case endExpired:
		return "expired"
	case endRevoked:
		return "permission_revoked"
	case endReplaced:
		return "replaced"
	case endByteLimit:
		return "byte_limit"
	case endFailed:
		return "upstream_failure"
	case endServerShutdown:
		return "server_shutdown"
	default:
		return "unknown"
	}
}

func (service *Service) clearAccessCookie(writer http.ResponseWriter) {
	http.SetCookie(writer, &http.Cookie{Name: accessCookieName, Path: "/", Expires: time.Unix(1, 0).UTC(),
		MaxAge: -1, HttpOnly: true, Secure: service.secure, SameSite: http.SameSiteLaxMode})
}

func accessErrorStatus(err error) int {
	if errors.Is(err, ErrAccessFailed) {
		return http.StatusBadGateway
	}
	if errors.Is(err, ErrByteLimit) {
		return http.StatusInsufficientStorage
	}
	return http.StatusUnauthorized
}

func (service *Service) writeAccessRequired(writer http.ResponseWriter, status int, err error) {
	title := "此 Pod 访问地址已失效"
	lead := "该地址无法继续访问，可能已经使用、过期，或当前登录与权限已被收回。"
	noticeTitle := "请返回 ZKE Console"
	noticeBody := "关闭此页面，在原 Pod 详情中重新创建访问地址。出于安全考虑，失效地址不能恢复或重复激活。"
	footnote := "如果访问权限刚刚发生变化，请刷新 Console 并确认当前账号仍具有 Pod 端口转发权限。"
	switch {
	case errors.Is(err, ErrAccessReplaced):
		title = "此 Pod 访问会话已被替换"
		lead = "已为同一个 Pod 创建新的访问地址，当前旧入口已主动结束。"
		noticeBody = "请关闭此页面，改用 ZKE Console 中最近创建的 Pod 访问地址。"
		footnote = "同一个 Pod 同时只保留一个待激活地址或访问会话，避免多个入口长期占用连接与权限。"
	case errors.Is(err, ErrByteLimit):
		title = "此 Pod 访问会话已达到流量上限"
		lead = "为保护 Server 与 Agent，当前会话的累计请求或响应流量已达到配置上限。"
		noticeBody = "请关闭此页面，并从原 Pod 详情重新创建访问地址；如持续发生，请联系管理员评估 Pod Access 流量上限。"
		footnote = "普通刷新不会立即用尽默认上限；大文件下载、持续流式响应或大量资源请求会累计计入会话流量。"
	case errors.Is(err, ErrAccessRevoked):
		title = "此 Pod 访问权限已被收回"
		lead = "当前登录 Session 已失效，或账号不再具有该集群的 Pod 端口转发权限。"
		noticeBody = "请返回 ZKE Console，重新登录或确认权限后再创建访问地址。"
		footnote = "权限由 Server 周期复核，已建立的连接也会在权限收回后关闭。"
	case errors.Is(err, ErrAccessFailed):
		title = "Pod 服务连接已中断"
		lead = "Pod 已被替换、Agent 连接中断，或上游端口转发无法继续。"
		noticeBody = "请返回 ZKE Console，确认 Pod 状态和端口后重新创建访问地址。"
		footnote = "ZKE 不会把已中断的入口静默切换到同名新建的 Pod。"
	}
	setPodAccessPageHeaders(writer)
	writer.WriteHeader(status)
	_, _ = io.WriteString(writer, podAccessPageStart+`<title>`+html.EscapeString(title)+` · ZKE</title>
</head>
<body>
<main class="status-card">`+podAccessBrand+`
  <div class="status-icon danger" aria-hidden="true"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="9"/><path d="m9 9 6 6m0-6-6 6"/></svg></div>
	<h1>`+html.EscapeString(title)+`</h1>
	<p class="lead">`+html.EscapeString(lead)+`</p>
	<div class="notice"><svg aria-hidden="true" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M12 3a9 9 0 1 0 9 9"/><path d="M12 7v5l3 2"/></svg><div><strong>`+html.EscapeString(noticeTitle)+`</strong><p>`+html.EscapeString(noticeBody)+`</p></div></div>
	<p class="footnote">`+html.EscapeString(footnote)+`</p>
</main>
</body>
</html>`)
}

func setPodAccessPageHeaders(writer http.ResponseWriter) {
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
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
			reason := endExpired
			if time.Now().Before(session.expiresAt) {
				reason = endServerShutdown
			}
			session.service.deactivate(session.key, reason)
			return
		case now := <-ticker.C:
			if err := session.service.revalidate(session.ctx, session.input, now.UTC()); err != nil {
				session.service.deactivate(session.key, endRevoked)
				return
			}
		}
	}
}

func (session *activeSession) close() {
	session.closeOnce.Do(func() {
		session.cancel()
		if session.transport != nil {
			session.transport.CloseIdleConnections()
		}
	})
}

func (session *activeSession) fail(reason endReason) {
	session.failureOnce.Do(func() {
		session.service.deactivate(session.key, reason)
	})
}

func (session *activeSession) newTransport() *http.Transport {
	return &http.Transport{
		Proxy:                  nil,
		DialContext:            session.dialContext,
		ForceAttemptHTTP2:      false,
		MaxIdleConns:           session.service.config.MaxConnectionsPerSession,
		MaxIdleConnsPerHost:    session.service.config.MaxConnectionsPerSession,
		MaxConnsPerHost:        session.service.config.MaxConnectionsPerSession,
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
	startedAt := time.Now()
	if err := session.acquire(ctx); err != nil {
		session.logUpstreamResult(podportforward.Result{}, err, time.Since(startedAt))
		return nil, err
	}
	client, peerConnection := net.Pipe()
	managed := &managedConnection{Conn: client, release: session.release}
	peer := &pipePeer{connection: peerConnection}
	go func() {
		result, err := session.service.forwarder.Run(session.ctx, session.forward, peer,
			session.service.config.MaxClientBytes, session.service.config.MaxPodBytes)
		_ = peerConnection.Close()
		_ = managed.Close()
		session.logUpstreamResult(result, err, time.Since(startedAt))
		switch {
		case errors.Is(err, podportforward.ErrByteLimit):
			session.fail(endByteLimit)
		case errors.Is(err, podportforward.ErrPodReplaced), errors.Is(err, podportforward.ErrClusterAccessDenied):
			session.fail(endFailed)
		}
	}()
	return &limitedConnection{Conn: managed, session: session}, nil
}

func (session *activeSession) logUpstreamResult(result podportforward.Result, err error, duration time.Duration) {
	attributes := append(
		podAccessSessionLogAttributes(session),
		slog.Duration("duration", duration),
		slog.Uint64("client_bytes", result.ClientBytes),
		slog.Uint64("pod_bytes", result.PodBytes),
		slog.String("reason", podAccessUpstreamReason(err)),
	)
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, ErrAccessExpired) {
		session.service.logger.Debug("Pod access upstream closed", attributes...)
		return
	}
	attributes = append(attributes, slog.String("error", err.Error()))
	session.service.logger.Warn("Pod access upstream failed", attributes...)
}

func podAccessUpstreamReason(err error) string {
	switch {
	case err == nil:
		return "completed"
	case errors.Is(err, context.Canceled):
		return "session_closed"
	case errors.Is(err, ErrAccessExpired):
		return "session_closed"
	case errors.Is(err, ErrCapacity):
		return "capacity_exhausted"
	case errors.Is(err, podportforward.ErrByteLimit):
		return "byte_limit"
	case errors.Is(err, podportforward.ErrPodReplaced):
		return "pod_replaced"
	case errors.Is(err, podportforward.ErrAgentNotConnected):
		return "agent_disconnected"
	case errors.Is(err, podportforward.ErrAgentUnsupported):
		return "agent_unsupported"
	case errors.Is(err, podportforward.ErrRequestCapacity):
		return "capacity_exhausted"
	case errors.Is(err, podportforward.ErrPodNotFound):
		return "pod_not_found"
	case errors.Is(err, podportforward.ErrClusterUnauthenticated):
		return "kubernetes_unauthenticated"
	case errors.Is(err, podportforward.ErrClusterAccessDenied):
		return "kubernetes_forbidden"
	case errors.Is(err, podportforward.ErrClusterUnavailable):
		return "kubernetes_unavailable"
	case errors.Is(err, podportforward.ErrClusterTimeout):
		return "timeout"
	case errors.Is(err, podportforward.ErrInvalidResponse):
		return "invalid_agent_response"
	default:
		return "upstream_failure"
	}
}

func (session *activeSession) acquire(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-session.ctx.Done():
		return ErrAccessExpired
	case session.connections <- struct{}{}:
	}
	select {
	case <-ctx.Done():
		<-session.connections
		return ctx.Err()
	case <-session.ctx.Done():
		<-session.connections
		return ErrAccessExpired
	case session.service.connections <- struct{}{}:
		return nil
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
		connection.session.fail(endByteLimit)
		return 0, ErrByteLimit
	}
	return connection.Conn.Write(data)
}

func (connection *limitedConnection) Read(data []byte) (int, error) {
	read, err := connection.Conn.Read(data)
	if read > 0 && exceedsLimit(&connection.session.podBytes, uint64(read), connection.session.service.config.MaxPodBytes) {
		connection.session.fail(endByteLimit)
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

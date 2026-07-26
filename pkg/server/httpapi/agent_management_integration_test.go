package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/togettoyou/zke/pkg/server/agentconn"
	"github.com/togettoyou/zke/pkg/server/agentmanagement"
	"github.com/togettoyou/zke/pkg/server/agentstatus"
	"github.com/togettoyou/zke/pkg/server/audit"
	"github.com/togettoyou/zke/pkg/server/auth"
	"github.com/togettoyou/zke/pkg/server/rbac"
	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/server/store/migrations"
)

type fakeAgentConnections struct {
	statuses map[string]agentconn.ConnectionStatus
}

func (connections *fakeAgentConnections) Snapshot(
	agentIDs []string,
) map[string]agentconn.ConnectionStatus {
	result := make(map[string]agentconn.ConnectionStatus, len(agentIDs))
	for _, agentID := range agentIDs {
		if status, exists := connections.statuses[agentID]; exists {
			result[agentID] = status
		}
	}
	return result
}

func TestAgentManagementHTTPFlow(t *testing.T) {
	databaseURL := requireHTTPTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := openHTTPTestDatabase(t, ctx, databaseURL)
	if _, err := migrations.Apply(ctx, pool); err != nil {
		t.Fatal(err)
	}

	authStore := store.NewAuthStore(pool)
	password := []byte("a sufficiently long Agent administrator passphrase")
	admin, err := auth.CreateInitialAdmin(ctx, authStore, auth.InitialAdminInput{
		Username:    "agent-admin",
		DisplayName: "Agent Administrator",
		Password:    password,
	})
	if err != nil {
		t.Fatal(err)
	}
	passwordHash, err := auth.HashPassword(
		password,
		auth.DefaultPasswordParams(),
	)
	if err != nil {
		t.Fatal(err)
	}
	var viewerID string
	if err := pool.QueryRow(ctx, `
INSERT INTO users (
    id, username_normalized, display_name, password_hash, status,
    password_changed_at
)
VALUES (
    gen_random_uuid(), 'agent-viewer', 'Agent Viewer', $1, 'active', now()
)
RETURNING id::text
`, passwordHash).Scan(&viewerID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO role_bindings (id, subject_id, role, scope_type)
VALUES (gen_random_uuid(), $1, 'viewer', 'global')
`, viewerID); err != nil {
		t.Fatal(err)
	}

	var tenantID, projectID, clusterID, agentID string
	if err := pool.QueryRow(ctx, `
INSERT INTO tenants (id, name, status)
VALUES (gen_random_uuid(), 'Agent Tenant', 'active')
RETURNING id::text
`).Scan(&tenantID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
INSERT INTO projects (id, tenant_id, name, status)
VALUES (gen_random_uuid(), $1, 'Agent Project', 'active')
RETURNING id::text
`, tenantID).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
INSERT INTO clusters (id, tenant_id, project_id, name, status)
VALUES (gen_random_uuid(), $1, $2, 'Agent Cluster', 'active')
RETURNING id::text
`, tenantID, projectID).Scan(&clusterID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
INSERT INTO agents (
    id, tenant_id, project_id, cluster_id, version, protocol_version,
    lifecycle_status, health_status, active_credential_serial, last_seen_at
)
VALUES (
    gen_random_uuid(), $1, $2, $3, 'development', 'v1', 'active',
    'healthy', '42', now()
)
RETURNING id::text
`, tenantID, projectID, clusterID).Scan(&agentID); err != nil {
		t.Fatal(err)
	}
	certificateExpiresAt := time.Now().UTC().Add(30 * 24 * time.Hour)
	if _, err := pool.Exec(ctx, `
INSERT INTO agent_credentials (
    id, tenant_id, project_id, cluster_id, agent_id, serial,
    csr_fingerprint, certificate_pem, expires_at
)
VALUES (
    gen_random_uuid(), $1, $2, $3, $4, '42', decode('01', 'hex'),
    'certificate', $5
)
`, tenantID, projectID, clusterID, agentID, certificateExpiresAt); err != nil {
		t.Fatal(err)
	}

	authService := auth.NewService(authStore, auth.ServiceConfig{
		SessionIdleTimeout:          30 * time.Minute,
		SessionAbsoluteTimeout:      8 * time.Hour,
		MaxConcurrentPasswordChecks: 1,
	})
	adminLogin, err := authService.Login(ctx, auth.LoginInput{
		Username:  admin.Username,
		Password:  password,
		RequestID: "request-agent-admin-login",
		Now:       time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	viewerLogin, err := authService.Login(ctx, auth.LoginInput{
		Username:  "agent-viewer",
		Password:  password,
		RequestID: "request-agent-viewer-login",
		Now:       time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}

	connectedAt := time.Now().UTC().Add(-time.Minute)
	heartbeatAt := time.Now().UTC()
	connections := &fakeAgentConnections{statuses: map[string]agentconn.ConnectionStatus{
		agentID: {
			State:           "online",
			ConnectionID:    "connection-1",
			ConnectedAt:     connectedAt,
			LastHeartbeatAt: heartbeatAt,
		},
	}}
	auditService := audit.NewService(store.NewAuditStore(pool))
	router := New(
		discardLogger(),
		Dependencies{
			ReadinessCheck: pool.Ping,
			AuthService:    authService,
			AuditService:   auditService,
			RBACService:    rbac.NewService(store.NewRBACStore(pool)),
			AgentManagementService: agentmanagement.NewService(
				store.NewAgentManagementStore(pool),
			),
			AgentStatusService: agentstatus.NewService(
				store.NewAgentStatusStore(pool),
				connections,
				7*24*time.Hour,
			),
		},
		Config{Authentication: defaultAuthenticationTestConfig()},
	)

	listPath := "/api/v1/projects/" + projectID + "/agents"
	listResponse := authenticatedRequest(
		t,
		router,
		http.MethodGet,
		listPath,
		"",
		adminLogin,
		false,
	)
	if listResponse.Code != http.StatusOK {
		t.Fatalf("list status = %d: %s", listResponse.Code, listResponse.Body)
	}
	if listResponse.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf(
			"Agent status Cache-Control = %q, want no-store",
			listResponse.Header().Get("Cache-Control"),
		)
	}
	var listed struct {
		Agents []agentStatusResponse `json:"agents"`
	}
	if err := json.Unmarshal(listResponse.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Agents) != 1 ||
		listed.Agents[0].ConnectionStatus != "online" ||
		listed.Agents[0].ConnectionID != "connection-1" ||
		listed.Agents[0].ConnectedAt == nil ||
		listed.Agents[0].LastHeartbeatAt == nil {
		t.Fatalf("unexpected online Agent status: %+v", listed.Agents)
	}
	assertUTC8TimePointer(t, "last_seen_at", listed.Agents[0].LastSeenAt)
	assertUTC8Time(
		t,
		"certificate_expires_at",
		listed.Agents[0].CertificateExpiresAt,
	)
	assertUTC8TimePointer(t, "connected_at", listed.Agents[0].ConnectedAt)
	assertUTC8TimePointer(t, "last_heartbeat_at", listed.Agents[0].LastHeartbeatAt)

	revokePath := "/api/v1/agents/" + agentID + "/revoke"
	missingCSRF := authenticatedRequest(
		t,
		router,
		http.MethodPost,
		revokePath,
		`{"confirm":true}`,
		adminLogin,
		false,
	)
	if missingCSRF.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF status = %d, want 403", missingCSRF.Code)
	}

	viewerDenied := authenticatedRequest(
		t,
		router,
		http.MethodPost,
		revokePath,
		`{"confirm":true}`,
		viewerLogin,
		true,
	)
	if viewerDenied.Code != http.StatusForbidden {
		t.Fatalf("viewer revoke status = %d, want 403", viewerDenied.Code)
	}

	confirmationRequired := authenticatedRequest(
		t,
		router,
		http.MethodPost,
		revokePath,
		`{"confirm":false}`,
		adminLogin,
		true,
	)
	if confirmationRequired.Code != http.StatusBadRequest {
		t.Fatalf(
			"confirmation status = %d, want 400",
			confirmationRequired.Code,
		)
	}
	assertErrorCode(t, confirmationRequired, "confirmation_required")

	invalidAgent := authenticatedRequest(
		t,
		router,
		http.MethodPost,
		"/api/v1/agents/not-a-uuid/revoke",
		`{"confirm":true}`,
		adminLogin,
		true,
	)
	if invalidAgent.Code != http.StatusBadRequest {
		t.Fatalf("invalid Agent status = %d, want 400", invalidAgent.Code)
	}
	assertErrorCode(t, invalidAgent, "invalid_request")

	missingAgentID := "00000000-0000-4000-8000-000000000099"
	missingAgent := authenticatedRequest(
		t,
		router,
		http.MethodPost,
		"/api/v1/agents/"+missingAgentID+"/revoke",
		`{"confirm":true}`,
		adminLogin,
		true,
	)
	if missingAgent.Code != http.StatusForbidden {
		t.Fatalf("missing Agent status = %d, want 403", missingAgent.Code)
	}
	assertErrorCode(t, missingAgent, "forbidden")

	revoked := authenticatedRequest(
		t,
		router,
		http.MethodPost,
		revokePath,
		`{"confirm":true}`,
		adminLogin,
		true,
	)
	if revoked.Code != http.StatusOK {
		t.Fatalf("revoke status = %d: %s", revoked.Code, revoked.Body)
	}
	var revokedBody revokeAgentResponse
	if err := json.Unmarshal(revoked.Body.Bytes(), &revokedBody); err != nil {
		t.Fatal(err)
	}
	if revokedBody.AgentID != agentID ||
		revokedBody.LifecycleStatus != "revoked" ||
		revokedBody.AlreadyRevoked ||
		revokedBody.RevokedAt.IsZero() {
		t.Fatalf("unexpected revoke response: %+v", revokedBody)
	}
	assertUTC8Time(t, "revoked_at", revokedBody.RevokedAt)

	disconnectedAt := time.Now().UTC()
	connections.statuses[agentID] = agentconn.ConnectionStatus{
		State:                "offline",
		LastHeartbeatAt:      heartbeatAt,
		LastDisconnectedAt:   disconnectedAt,
		LastDisconnectReason: "agent_revoked",
	}
	offlineResponse := authenticatedRequest(
		t,
		router,
		http.MethodGet,
		listPath,
		"",
		adminLogin,
		false,
	)
	if offlineResponse.Code != http.StatusOK {
		t.Fatalf("offline list status = %d", offlineResponse.Code)
	}
	listed.Agents = nil
	if err := json.Unmarshal(offlineResponse.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Agents) != 1 ||
		listed.Agents[0].LifecycleStatus != "revoked" ||
		listed.Agents[0].CertificateStatus != "revoked" ||
		listed.Agents[0].ConnectionStatus != "offline" ||
		listed.Agents[0].LastDisconnectedAt == nil ||
		listed.Agents[0].LastDisconnectReason != "agent_revoked" {
		t.Fatalf("unexpected offline Agent status: %+v", listed.Agents)
	}
	assertUTC8TimePointer(
		t,
		"last_disconnected_at",
		listed.Agents[0].LastDisconnectedAt,
	)

	repeated := authenticatedRequest(
		t,
		router,
		http.MethodPost,
		revokePath,
		`{"confirm":true}`,
		adminLogin,
		true,
	)
	if repeated.Code != http.StatusOK {
		t.Fatalf("repeated revoke status = %d: %s", repeated.Code, repeated.Body)
	}
	var repeatedBody revokeAgentResponse
	if err := json.Unmarshal(repeated.Body.Bytes(), &repeatedBody); err != nil {
		t.Fatal(err)
	}
	if !repeatedBody.AlreadyRevoked ||
		!repeatedBody.RevokedAt.Equal(revokedBody.RevokedAt) {
		t.Fatalf("unexpected repeated revoke response: %+v", repeatedBody)
	}
	assertUTC8Time(t, "repeated revoked_at", repeatedBody.RevokedAt)

	var deniedAudits, failedAudits, succeededAudits int
	if err := pool.QueryRow(ctx, `
SELECT
    count(*) FILTER (WHERE result = 'denied'),
    count(*) FILTER (WHERE result = 'failed'),
    count(*) FILTER (WHERE result = 'succeeded')
FROM audit_events
WHERE action = 'agent.revoke'
  AND target_id = $1
`, agentID).Scan(
		&deniedAudits,
		&failedAudits,
		&succeededAudits,
	); err != nil {
		t.Fatal(err)
	}
	if deniedAudits != 1 || failedAudits != 1 || succeededAudits != 2 {
		t.Fatalf(
			"Agent revoke audits denied/failed/succeeded = %d/%d/%d",
			deniedAudits,
			failedAudits,
			succeededAudits,
		)
	}
	var missingDeniedAudits int
	if err := pool.QueryRow(ctx, `
SELECT count(*)
FROM audit_events
WHERE action = 'agent.revoke'
  AND target_id = $1
  AND scope_type = 'global'
  AND result = 'denied'
`, missingAgentID).Scan(&missingDeniedAudits); err != nil {
		t.Fatal(err)
	}
	if missingDeniedAudits != 1 {
		t.Fatalf(
			"missing Agent denial audit count = %d, want 1",
			missingDeniedAudits,
		)
	}
}

func authenticatedRequest(
	t *testing.T,
	handler http.Handler,
	method string,
	path string,
	body string,
	login auth.LoginResult,
	withCSRF bool,
) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.AddCookie(&http.Cookie{
		Name:  sessionCookieName,
		Value: login.SessionToken,
	})
	if withCSRF {
		request.Header.Set(csrfHeaderName, login.CSRFToken)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	handler.ServeHTTP(response, request)
	return response
}

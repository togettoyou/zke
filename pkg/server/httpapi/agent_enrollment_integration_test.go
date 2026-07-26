package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/togettoyou/zke/pkg/server/agentinstall"
	"github.com/togettoyou/zke/pkg/server/audit"
	"github.com/togettoyou/zke/pkg/server/auth"
	"github.com/togettoyou/zke/pkg/server/enrollment"
	"github.com/togettoyou/zke/pkg/server/rbac"
	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/server/store/migrations"
)

func TestCreateAgentEnrollmentHTTPFlow(t *testing.T) {
	databaseURL := requireHTTPTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool := openHTTPTestDatabase(t, ctx, databaseURL)
	if _, err := migrations.Apply(ctx, pool); err != nil {
		t.Fatal(err)
	}

	authStore := store.NewAuthStore(pool)
	password := []byte("a sufficiently long enrollment admin passphrase")
	admin, err := auth.CreateInitialAdmin(ctx, authStore, auth.InitialAdminInput{
		Username:    "enrollment-admin",
		DisplayName: "Enrollment Administrator",
		Password:    password,
	})
	if err != nil {
		t.Fatal(err)
	}

	var tenantID, projectID string
	if err := pool.QueryRow(ctx, `
INSERT INTO tenants (id, name, status)
VALUES (gen_random_uuid(), 'Enrollment Tenant', 'active')
RETURNING id::text
`).Scan(&tenantID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
INSERT INTO projects (id, tenant_id, name, status)
VALUES (gen_random_uuid(), $1, 'Enrollment Project', 'active')
RETURNING id::text
`, tenantID).Scan(&projectID); err != nil {
		t.Fatal(err)
	}

	authService := auth.NewService(authStore, auth.ServiceConfig{
		SessionIdleTimeout:          30 * time.Minute,
		SessionAbsoluteTimeout:      8 * time.Hour,
		MaxConcurrentPasswordChecks: 1,
	})
	login, err := authService.Login(ctx, auth.LoginInput{
		Username:  admin.Username,
		Password:  password,
		RequestID: "request-enrollment-admin-login",
		Now:       time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}

	enrollmentService := enrollment.NewService(
		store.NewEnrollmentStore(pool),
		enrollment.ServiceConfig{TokenTTL: enrollment.DefaultTokenTTL},
	)
	installationService := agentinstall.NewService(
		enrollmentService,
		agentinstall.Config{
			Enabled:                  true,
			PublicHTTPURL:            "https://zke.example.com",
			PublicQUICAddress:        "zke.example.com:8443",
			Image:                    "registry.example.com/zke-agent:test",
			Namespace:                "zke-system",
			ImagePullPolicy:          corev1.PullIfNotPresent,
			ListenerCACertificatePEM: []byte("test-listener-ca"),
		},
	)
	auditService := audit.NewService(store.NewAuditStore(pool))
	router := New(
		discardLogger(),
		Dependencies{
			ReadinessCheck:           pool.Ping,
			AuthService:              authService,
			AuditService:             auditService,
			RBACService:              rbac.NewService(store.NewRBACStore(pool)),
			EnrollmentService:        enrollmentService,
			AgentInstallationService: installationService,
		},
		Config{Authentication: defaultAuthenticationTestConfig()},
	)
	path := "/api/v1/projects/" + projectID + "/cluster-enrollments"
	requestBody := `{"cluster_name":"integration-cluster"}`

	missingCSRFResponse := httptest.NewRecorder()
	missingCSRFRequest := httptest.NewRequest(http.MethodPost, path, nil)
	missingCSRFRequest.AddCookie(&http.Cookie{
		Name:  sessionCookieName,
		Value: login.SessionToken,
	})
	router.ServeHTTP(missingCSRFResponse, missingCSRFRequest)
	if missingCSRFResponse.Code != http.StatusForbidden {
		t.Fatalf(
			"missing CSRF status = %d, want %d: %s",
			missingCSRFResponse.Code,
			http.StatusForbidden,
			missingCSRFResponse.Body,
		)
	}
	assertErrorCode(t, missingCSRFResponse, "csrf_invalid")

	missingIdempotencyResponse := httptest.NewRecorder()
	missingIdempotencyRequest := httptest.NewRequest(
		http.MethodPost,
		path,
		strings.NewReader(requestBody),
	)
	missingIdempotencyRequest.AddCookie(&http.Cookie{
		Name:  sessionCookieName,
		Value: login.SessionToken,
	})
	missingIdempotencyRequest.Header.Set(csrfHeaderName, login.CSRFToken)
	router.ServeHTTP(missingIdempotencyResponse, missingIdempotencyRequest)
	if missingIdempotencyResponse.Code != http.StatusBadRequest {
		t.Fatalf(
			"missing idempotency key status = %d, want %d: %s",
			missingIdempotencyResponse.Code,
			http.StatusBadRequest,
			missingIdempotencyResponse.Body,
		)
	}
	assertErrorCode(t, missingIdempotencyResponse, "invalid_request")
	var failedAuditCount int
	if err := pool.QueryRow(ctx, `
SELECT count(*)
FROM audit_events
WHERE action = 'cluster.enrollment.create'
  AND project_id = $1
  AND actor_user_id = $2
  AND result = 'failed'
`, projectID, admin.ID).Scan(&failedAuditCount); err != nil {
		t.Fatal(err)
	}
	if failedAuditCount != 1 {
		t.Fatalf("failed enrollment audit count = %d, want 1", failedAuditCount)
	}

	idempotencyKey := "01234567-89ab-cdef-0123-456789abcdef"
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(requestBody))
	request.AddCookie(&http.Cookie{
		Name:  sessionCookieName,
		Value: login.SessionToken,
	})
	request.Header.Set(csrfHeaderName, login.CSRFToken)
	request.Header.Set(idempotencyKeyHeaderName, idempotencyKey)
	router.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf(
			"create enrollment status = %d, want %d: %s",
			response.Code,
			http.StatusCreated,
			response.Body,
		)
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", response.Header().Get("Cache-Control"))
	}

	var body createEnrollmentResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.ID == "" ||
		body.ClusterName != "integration-cluster" ||
		body.Token == "" ||
		body.ExpiresAt.IsZero() {
		t.Fatalf("incomplete enrollment response: %+v", body)
	}
	assertUTC8Time(t, "enrollment expires_at", body.ExpiresAt)

	expectedDigest := sha256.Sum256([]byte(body.Token))
	var storedDigest []byte
	var storedClusterName string
	if err := pool.QueryRow(ctx, `
SELECT token_digest, cluster_name
FROM enrollments
WHERE id = $1
  AND tenant_id = $2
  AND project_id = $3
  AND created_by_user_id = $4
`, body.ID, tenantID, projectID, admin.ID).Scan(
		&storedDigest,
		&storedClusterName,
	); err != nil {
		t.Fatal(err)
	}
	if string(storedDigest) != string(expectedDigest[:]) {
		t.Fatal("database did not store the returned token's SHA-256 digest")
	}
	if storedClusterName != "integration-cluster" {
		t.Fatalf("stored cluster name = %q, want integration-cluster", storedClusterName)
	}

	var auditCount int
	if err := pool.QueryRow(ctx, `
SELECT count(*)
FROM audit_events
WHERE action = 'cluster.enrollment.create'
  AND target_id = $1
  AND actor_user_id = $2
  AND tenant_id = $3
  AND project_id = $4
  AND result = 'succeeded'
`, body.ID, admin.ID, tenantID, projectID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 {
		t.Fatalf("enrollment audit count = %d, want 1", auditCount)
	}

	retryResponse := httptest.NewRecorder()
	retryRequest := httptest.NewRequest(
		http.MethodPost,
		path,
		strings.NewReader(requestBody),
	)
	retryRequest.AddCookie(&http.Cookie{
		Name:  sessionCookieName,
		Value: login.SessionToken,
	})
	retryRequest.Header.Set(csrfHeaderName, login.CSRFToken)
	retryRequest.Header.Set(idempotencyKeyHeaderName, idempotencyKey)
	router.ServeHTTP(retryResponse, retryRequest)
	if retryResponse.Code != http.StatusConflict {
		t.Fatalf(
			"retry status = %d, want %d: %s",
			retryResponse.Code,
			http.StatusConflict,
			retryResponse.Body,
		)
	}
	assertErrorCode(t, retryResponse, "idempotency_conflict")
	var enrollmentCount, succeededAuditCount int
	if err := pool.QueryRow(
		ctx,
		"SELECT count(*) FROM enrollments WHERE project_id = $1",
		projectID,
	).Scan(&enrollmentCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
SELECT count(*)
FROM audit_events
WHERE action = 'cluster.enrollment.create'
  AND project_id = $1
  AND result = 'succeeded'
`, projectID).Scan(&succeededAuditCount); err != nil {
		t.Fatal(err)
	}
	if enrollmentCount != 1 || succeededAuditCount != 1 {
		t.Fatalf(
			"retry created enrollments/audits = %d/%d, want 1/1",
			enrollmentCount,
			succeededAuditCount,
		)
	}

	enrollmentDetailResponse := httptest.NewRecorder()
	enrollmentDetailRequest := httptest.NewRequest(
		http.MethodGet,
		path+"/"+body.ID,
		nil,
	)
	enrollmentDetailRequest.AddCookie(&http.Cookie{
		Name: sessionCookieName, Value: login.SessionToken,
	})
	router.ServeHTTP(enrollmentDetailResponse, enrollmentDetailRequest)
	if enrollmentDetailResponse.Code != http.StatusOK {
		t.Fatalf(
			"get enrollment status = %d: %s",
			enrollmentDetailResponse.Code,
			enrollmentDetailResponse.Body,
		)
	}
	var enrollmentDetail enrollmentResponse
	if err := json.Unmarshal(enrollmentDetailResponse.Body.Bytes(), &enrollmentDetail); err != nil {
		t.Fatal(err)
	}
	if enrollmentDetail.ID != body.ID || enrollmentDetail.Status != "active" {
		t.Fatalf("unexpected enrollment detail: %+v", enrollmentDetail)
	}

	installationPath := "/api/v1/projects/" + projectID + "/cluster-installations"
	installationResponse := httptest.NewRecorder()
	installationRequest := httptest.NewRequest(
		http.MethodPost,
		installationPath,
		strings.NewReader(`{"cluster_name":"installed-cluster"}`),
	)
	installationRequest.AddCookie(&http.Cookie{
		Name:  sessionCookieName,
		Value: login.SessionToken,
	})
	installationRequest.Header.Set(csrfHeaderName, login.CSRFToken)
	installationRequest.Header.Set(
		idempotencyKeyHeaderName,
		"11111111-2222-3333-4444-555555555555",
	)
	router.ServeHTTP(installationResponse, installationRequest)
	if installationResponse.Code != http.StatusCreated {
		t.Fatalf(
			"create installation status = %d, want %d: %s",
			installationResponse.Code,
			http.StatusCreated,
			installationResponse.Body,
		)
	}
	var installationBody createAgentInstallationResponse
	if err := json.Unmarshal(
		installationResponse.Body.Bytes(),
		&installationBody,
	); err != nil {
		t.Fatal(err)
	}
	assertUTC8Time(t, "installation expires_at", installationBody.ExpiresAt)
	const bearerPrefix = "Authorization: Bearer "
	bearerStart := strings.Index(installationBody.InstallCommand, bearerPrefix)
	if bearerStart < 0 {
		t.Fatalf("install command is missing Bearer header: %s", installationBody.InstallCommand)
	}
	tokenStart := bearerStart + len(bearerPrefix)
	tokenEnd := strings.Index(installationBody.InstallCommand[tokenStart:], "'")
	if tokenEnd < 0 {
		t.Fatalf("install command has an invalid Bearer header: %s", installationBody.InstallCommand)
	}
	installationToken := installationBody.InstallCommand[tokenStart : tokenStart+tokenEnd]
	manifestResponse := httptest.NewRecorder()
	manifestRequest := httptest.NewRequest(
		http.MethodGet,
		"/agent-install/v1/manifest",
		nil,
	)
	manifestRequest.Header.Set("Authorization", "Bearer "+installationToken)
	router.ServeHTTP(manifestResponse, manifestRequest)
	if manifestResponse.Code != http.StatusOK {
		t.Fatalf(
			"manifest status = %d, want %d: %s",
			manifestResponse.Code,
			http.StatusOK,
			manifestResponse.Body,
		)
	}
	for _, required := range []string{
		"kind: Deployment",
		"name: zke-agent-enrollment",
		"server_address: zke.example.com:8443",
	} {
		if !strings.Contains(manifestResponse.Body.String(), required) {
			t.Errorf("manifest is missing %q", required)
		}
	}

	enrollmentListResponse := httptest.NewRecorder()
	enrollmentListRequest := httptest.NewRequest(http.MethodGet, path, nil)
	enrollmentListRequest.AddCookie(&http.Cookie{
		Name: sessionCookieName, Value: login.SessionToken,
	})
	router.ServeHTTP(enrollmentListResponse, enrollmentListRequest)
	if enrollmentListResponse.Code != http.StatusOK {
		t.Fatalf(
			"list enrollments status = %d: %s",
			enrollmentListResponse.Code,
			enrollmentListResponse.Body,
		)
	}
	var enrollmentList struct {
		Items []enrollmentResponse `json:"cluster_enrollments"`
	}
	if err := json.Unmarshal(enrollmentListResponse.Body.Bytes(), &enrollmentList); err != nil {
		t.Fatal(err)
	}
	if len(enrollmentList.Items) != 2 {
		t.Fatalf("listed enrollment count = %d, want 2", len(enrollmentList.Items))
	}

	revokeEnrollmentResponse := httptest.NewRecorder()
	revokeEnrollmentRequest := httptest.NewRequest(
		http.MethodDelete,
		path+"/"+installationBody.ID,
		strings.NewReader(`{"confirm":true}`),
	)
	revokeEnrollmentRequest.AddCookie(&http.Cookie{
		Name: sessionCookieName, Value: login.SessionToken,
	})
	revokeEnrollmentRequest.Header.Set(csrfHeaderName, login.CSRFToken)
	revokeEnrollmentRequest.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(revokeEnrollmentResponse, revokeEnrollmentRequest)
	if revokeEnrollmentResponse.Code != http.StatusOK {
		t.Fatalf(
			"revoke enrollment status = %d: %s",
			revokeEnrollmentResponse.Code,
			revokeEnrollmentResponse.Body,
		)
	}
	var revokedEnrollment enrollmentResponse
	if err := json.Unmarshal(revokeEnrollmentResponse.Body.Bytes(), &revokedEnrollment); err != nil {
		t.Fatal(err)
	}
	if revokedEnrollment.Status != "revoked" || revokedEnrollment.RevokedAt == nil {
		t.Fatalf("unexpected revoked enrollment: %+v", revokedEnrollment)
	}

	if _, err := pool.Exec(
		ctx,
		"UPDATE projects SET status = 'suspended' WHERE id = $1",
		projectID,
	); err != nil {
		t.Fatal(err)
	}
	suspendedResponse := httptest.NewRecorder()
	suspendedRequest := httptest.NewRequest(
		http.MethodPost,
		path,
		strings.NewReader(`{"cluster_name":"suspended-cluster"}`),
	)
	suspendedRequest.AddCookie(&http.Cookie{
		Name:  sessionCookieName,
		Value: login.SessionToken,
	})
	suspendedRequest.Header.Set(csrfHeaderName, login.CSRFToken)
	suspendedRequest.Header.Set(
		idempotencyKeyHeaderName,
		"fedcba98-7654-3210-fedc-ba9876543210",
	)
	router.ServeHTTP(suspendedResponse, suspendedRequest)
	if suspendedResponse.Code != http.StatusForbidden {
		t.Fatalf(
			"suspended project status = %d, want %d: %s",
			suspendedResponse.Code,
			http.StatusForbidden,
			suspendedResponse.Body,
		)
	}
	assertErrorCode(t, suspendedResponse, "forbidden")
	var deniedAuditCount int
	if err := pool.QueryRow(ctx, `
SELECT count(*)
FROM audit_events
WHERE action = 'cluster.enrollment.create'
  AND project_id = $1
  AND actor_user_id = $2
  AND result = 'denied'
`, projectID, admin.ID).Scan(&deniedAuditCount); err != nil {
		t.Fatal(err)
	}
	if deniedAuditCount != 1 {
		t.Fatalf("denied enrollment audit count = %d, want 1", deniedAuditCount)
	}

	invalidResponse := httptest.NewRecorder()
	invalidRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/projects/not-a-uuid/cluster-enrollments",
		nil,
	)
	invalidRequest.AddCookie(&http.Cookie{
		Name:  sessionCookieName,
		Value: login.SessionToken,
	})
	invalidRequest.Header.Set(csrfHeaderName, login.CSRFToken)
	router.ServeHTTP(invalidResponse, invalidRequest)
	if invalidResponse.Code != http.StatusBadRequest {
		t.Fatalf(
			"invalid project status = %d, want %d: %s",
			invalidResponse.Code,
			http.StatusBadRequest,
			invalidResponse.Body,
		)
	}
	assertErrorCode(t, invalidResponse, "invalid_request")
}

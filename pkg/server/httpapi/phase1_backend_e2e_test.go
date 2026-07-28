package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/togettoyou/zke/pkg/server/accessmanagement"
	"github.com/togettoyou/zke/pkg/server/agentconn"
	"github.com/togettoyou/zke/pkg/server/agentinstall"
	"github.com/togettoyou/zke/pkg/server/agentmanagement"
	"github.com/togettoyou/zke/pkg/server/agentstatus"
	"github.com/togettoyou/zke/pkg/server/audit"
	"github.com/togettoyou/zke/pkg/server/auditaction"
	"github.com/togettoyou/zke/pkg/server/auth"
	"github.com/togettoyou/zke/pkg/server/enrollment"
	"github.com/togettoyou/zke/pkg/server/rbac"
	"github.com/togettoyou/zke/pkg/server/resourcemanagement"
	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/server/store/migrations"
)

type phase1ConnectionSnapshots struct {
	statuses map[string]agentconn.ConnectionStatus
}

func (snapshots *phase1ConnectionSnapshots) Snapshot(
	agentIDs []string,
) map[string]agentconn.ConnectionStatus {
	result := make(map[string]agentconn.ConnectionStatus, len(agentIDs))
	for _, agentID := range agentIDs {
		if status, exists := snapshots.statuses[agentID]; exists {
			result[agentID] = status
		}
	}
	return result
}

func (*phase1ConnectionSnapshots) PublishAgentStatusChange(
	string,
	string,
	string,
	string,
) {
}

func TestPhase1BackendEndToEnd(t *testing.T) {
	databaseURL := requireHTTPTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := openHTTPTestDatabase(t, ctx, databaseURL)
	if _, err := migrations.Apply(ctx, pool); err != nil {
		t.Fatal(err)
	}

	authStore := store.NewAuthStore(pool)
	const adminPassword = "a sufficiently long Phase 1 administrator password"
	admin, err := auth.CreateInitialAdmin(ctx, authStore, auth.InitialAdminInput{
		Username:    "phase1-admin",
		DisplayName: "Phase 1 Administrator",
		Password:    []byte(adminPassword),
	})
	if err != nil {
		t.Fatal(err)
	}
	authService := auth.NewService(authStore, auth.ServiceConfig{
		SessionIdleTimeout:          30 * time.Minute,
		SessionAbsoluteTimeout:      8 * time.Hour,
		MaxConcurrentPasswordChecks: 1,
	})
	rbacService := rbac.NewService(store.NewRBACStore(pool))
	auditService := audit.NewService(store.NewAuditStore(pool), rbacService)
	now := time.Now().UTC()
	caCertificatePEM, caPrivateKeyPEM, _ := createHTTPTestAgentCA(t, now)
	certificateSigner, err := enrollment.NewCertificateSigner(
		caCertificatePEM,
		caPrivateKeyPEM,
		24*time.Hour,
	)
	if err != nil {
		t.Fatal(err)
	}
	enrollmentService := enrollment.NewService(
		store.NewEnrollmentStore(pool),
		enrollment.ServiceConfig{
			TokenTTL:          enrollment.DefaultTokenTTL,
			CertificateSigner: certificateSigner,
		},
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
			ListenerCACertificatePEM: caCertificatePEM,
		},
	)
	connections := &phase1ConnectionSnapshots{
		statuses: make(map[string]agentconn.ConnectionStatus),
	}
	router := New(
		discardLogger(),
		Dependencies{
			ReadinessCheck:           pool.Ping,
			AuthService:              authService,
			AuditService:             auditService,
			RBACService:              rbacService,
			EnrollmentService:        enrollmentService,
			AgentInstallationService: installationService,
			AgentManagementService: agentmanagement.NewService(
				store.NewAgentManagementStore(pool),
				connections,
			),
			AgentStatusService: agentstatus.NewService(
				store.NewAgentStatusStore(pool),
				connections,
				// This flow never opens the Cluster event stream, so it needs
				// no event source.
				nil,
				7*24*time.Hour,
			),
			ResourceManagementService: resourcemanagement.NewService(
				store.NewResourceManagementStore(pool),
				rbacService,
			),
			AccessManagementService: accessmanagement.NewService(
				store.NewAccessManagementStore(pool),
				accessmanagement.Config{MaxConcurrentPasswordHashes: 1},
			),
		},
		Config{
			Authentication: defaultAuthenticationTestConfig(),
			AgentEnrollment: AgentEnrollmentHTTPConfig{
				OperationTimeout:     5 * time.Second,
				RateLimitWindow:      time.Minute,
				MaxAttemptsPerSource: 100,
			},
		},
	)

	loginResponse := httptest.NewRecorder()
	loginRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/auth/login",
		strings.NewReader(
			`{"username":"phase1-admin","password":"`+adminPassword+`"}`,
		),
	)
	loginRequest.Header.Set("Content-Type", "application/json")
	loginRequest.RemoteAddr = "192.0.2.80:1234"
	router.ServeHTTP(loginResponse, loginRequest)
	requirePhase1Status(t, loginResponse, http.StatusOK, "login")
	sessionCookie := findCookie(
		t,
		loginResponse.Result().Cookies(),
		sessionCookieName,
	)
	csrfCookie := findCookie(
		t,
		loginResponse.Result().Cookies(),
		csrfCookieName,
	)

	const userPassword = "a sufficiently long Phase 1 managed user password"
	createdUser := phase1APIRequest(
		router, http.MethodPost, "/api/v1/users",
		`{"username":"phase1-user","display_name":"Phase 1 User","password":"`+
			userPassword+`"}`,
		sessionCookie, csrfCookie.Value, "",
	)
	requirePhase1Status(t, createdUser, http.StatusCreated, "create user")
	var user managedUserResponse
	decodePhase1Response(t, createdUser, &user)
	updatedUser := phase1APIRequest(
		router, http.MethodPut, "/api/v1/users/"+user.ID,
		`{"display_name":"Updated Phase 1 User"}`,
		sessionCookie, csrfCookie.Value, "",
	)
	requirePhase1Status(t, updatedUser, http.StatusOK, "update user")
	userDetail := phase1APIRequest(
		router, http.MethodGet, "/api/v1/users/"+user.ID, "",
		sessionCookie, "", "",
	)
	requirePhase1Status(t, userDetail, http.StatusOK, "get user")

	createdTenant := phase1APIRequest(
		router, http.MethodPost, "/api/v1/tenants",
		`{"name":"Phase 1 Tenant"}`,
		sessionCookie, csrfCookie.Value, "phase1-tenant-create-0001",
	)
	requirePhase1Status(t, createdTenant, http.StatusCreated, "create tenant")
	var tenant tenantResponse
	decodePhase1Response(t, createdTenant, &tenant)
	tenantDetail := phase1APIRequest(
		router, http.MethodGet, "/api/v1/tenants/"+tenant.ID, "",
		sessionCookie, "", "",
	)
	requirePhase1Status(t, tenantDetail, http.StatusOK, "get tenant")
	updatedTenant := phase1APIRequest(
		router, http.MethodPut, "/api/v1/tenants/"+tenant.ID,
		`{"name":"Updated Phase 1 Tenant","status":"active","confirm":true}`,
		sessionCookie, csrfCookie.Value, "",
	)
	requirePhase1Status(t, updatedTenant, http.StatusOK, "update tenant")

	projectCollectionPath := "/api/v1/tenants/" + tenant.ID + "/projects"
	createdProject := phase1APIRequest(
		router, http.MethodPost, projectCollectionPath,
		`{"name":"Phase 1 Project"}`,
		sessionCookie, csrfCookie.Value, "phase1-project-create-0001",
	)
	requirePhase1Status(t, createdProject, http.StatusCreated, "create project")
	var project projectResponse
	decodePhase1Response(t, createdProject, &project)
	projectDetail := phase1APIRequest(
		router, http.MethodGet, "/api/v1/projects/"+project.ID, "",
		sessionCookie, "", "",
	)
	requirePhase1Status(t, projectDetail, http.StatusOK, "get project")
	updatedProject := phase1APIRequest(
		router, http.MethodPut, "/api/v1/projects/"+project.ID,
		`{"name":"Updated Phase 1 Project","status":"active","confirm":true}`,
		sessionCookie, csrfCookie.Value, "",
	)
	requirePhase1Status(t, updatedProject, http.StatusOK, "update project")

	createdBinding := phase1APIRequest(
		router, http.MethodPost, "/api/v1/role-bindings",
		`{"subject_id":"`+user.ID+`","role":"viewer","scope_type":"project",`+
			`"tenant_id":"`+tenant.ID+`","project_id":"`+project.ID+`",`+
			`"confirm":true}`,
		sessionCookie, csrfCookie.Value, "",
	)
	requirePhase1Status(
		t,
		createdBinding,
		http.StatusCreated,
		"create role binding",
	)
	var binding roleBindingResponse
	decodePhase1Response(t, createdBinding, &binding)
	bindingDetail := phase1APIRequest(
		router, http.MethodGet, "/api/v1/role-bindings/"+binding.ID, "",
		sessionCookie, "", "",
	)
	requirePhase1Status(
		t,
		bindingDetail,
		http.StatusOK,
		"get role binding",
	)

	installation := phase1APIRequest(
		router,
		http.MethodPost,
		"/api/v1/projects/"+project.ID+"/cluster-installations",
		`{"cluster_name":"Phase 1 Installation Preview"}`,
		sessionCookie,
		csrfCookie.Value,
		"phase1-installation-create-0001",
	)
	requirePhase1Status(
		t,
		installation,
		http.StatusCreated,
		"create cluster installation",
	)
	var installationBody createAgentInstallationResponse
	decodePhase1Response(t, installation, &installationBody)
	installationToken := phase1InstallationToken(
		t,
		installationBody.InstallCommand,
	)
	manifest := httptest.NewRecorder()
	manifestRequest := httptest.NewRequest(
		http.MethodGet,
		"/agent-install/v1/manifest",
		nil,
	)
	manifestRequest.Header.Set("Authorization", "Bearer "+installationToken)
	router.ServeHTTP(manifest, manifestRequest)
	requirePhase1Status(t, manifest, http.StatusOK, "get installation manifest")

	enrollmentPath := "/api/v1/projects/" + project.ID + "/cluster-enrollments"
	createdEnrollment := phase1APIRequest(
		router, http.MethodPost, enrollmentPath,
		`{"cluster_name":"Phase 1 Cluster"}`,
		sessionCookie, csrfCookie.Value, "phase1-enrollment-create-0001",
	)
	requirePhase1Status(
		t,
		createdEnrollment,
		http.StatusCreated,
		"create cluster enrollment",
	)
	var firstEnrollment createEnrollmentResponse
	decodePhase1Response(t, createdEnrollment, &firstEnrollment)
	enrollmentList := phase1APIRequest(
		router, http.MethodGet, enrollmentPath, "",
		sessionCookie, "", "",
	)
	requirePhase1Status(t, enrollmentList, http.StatusOK, "list enrollments")
	enrollmentDetail := phase1APIRequest(
		router, http.MethodGet, enrollmentPath+"/"+firstEnrollment.ID, "",
		sessionCookie, "", "",
	)
	requirePhase1Status(t, enrollmentDetail, http.StatusOK, "get enrollment")
	revokedPreview := phase1APIRequest(
		router,
		http.MethodDelete,
		enrollmentPath+"/"+installationBody.ID,
		`{"confirm":true}`,
		sessionCookie,
		csrfCookie.Value,
		"",
	)
	requirePhase1Status(
		t,
		revokedPreview,
		http.StatusOK,
		"revoke enrollment",
	)

	firstCSR, _ := createHTTPTestAgentCSR(t)
	firstRegistration := performAgentRegistrationRequest(
		t,
		router,
		firstEnrollment.Token,
		"phase1-enrollment-consume-0001",
		agentRegistrationRequest{
			CSRPEM:          string(firstCSR),
			AgentVersion:    "phase1-e2e",
			ProtocolVersion: "v1",
		},
	)
	requirePhase1Status(
		t,
		firstRegistration,
		http.StatusCreated,
		"register first cluster connection",
	)
	var firstIdentity agentRegistrationResponse
	decodePhase1Response(t, firstRegistration, &firstIdentity)

	clusterList := phase1APIRequest(
		router,
		http.MethodGet,
		"/api/v1/projects/"+project.ID+"/clusters",
		"",
		sessionCookie,
		"",
		"",
	)
	requirePhase1Status(t, clusterList, http.StatusOK, "list clusters")
	clusterDetail := phase1APIRequest(
		router,
		http.MethodGet,
		"/api/v1/clusters/"+firstIdentity.ClusterID,
		"",
		sessionCookie,
		"",
		"",
	)
	requirePhase1Status(t, clusterDetail, http.StatusOK, "get cluster")
	if strings.Contains(clusterDetail.Body.String(), `"agent_id"`) {
		t.Fatal("Cluster management response exposed the internal Agent ID")
	}
	var clusterAggregate agentStatusResponse
	decodePhase1Response(t, clusterDetail, &clusterAggregate)
	if clusterAggregate.ID != firstIdentity.ClusterID ||
		clusterAggregate.Connection.Version != "phase1-e2e" {
		t.Fatalf("unexpected Cluster aggregate: %+v", clusterAggregate)
	}
	updatedCluster := phase1APIRequest(
		router,
		http.MethodPut,
		"/api/v1/clusters/"+firstIdentity.ClusterID,
		`{"name":"Updated Phase 1 Cluster"}`,
		sessionCookie,
		csrfCookie.Value,
		"",
	)
	requirePhase1Status(t, updatedCluster, http.StatusOK, "update cluster")

	revokedConnection := phase1APIRequest(
		router,
		http.MethodPost,
		"/api/v1/clusters/"+firstIdentity.ClusterID+"/connection/revoke",
		`{"confirm":true}`,
		sessionCookie,
		csrfCookie.Value,
		"",
	)
	requirePhase1Status(
		t,
		revokedConnection,
		http.StatusOK,
		"revoke cluster connection",
	)
	reenrollment := phase1APIRequest(
		router,
		http.MethodPost,
		"/api/v1/clusters/"+firstIdentity.ClusterID+"/connection/reenroll",
		`{"confirm":true}`,
		sessionCookie,
		csrfCookie.Value,
		"phase1-cluster-reenroll-0001",
	)
	requirePhase1Status(
		t,
		reenrollment,
		http.StatusCreated,
		"create cluster reenrollment",
	)
	var reenrollmentBody createEnrollmentResponse
	decodePhase1Response(t, reenrollment, &reenrollmentBody)
	if reenrollmentBody.ClusterID != firstIdentity.ClusterID {
		t.Fatalf(
			"reenrollment cluster ID = %q, want %q",
			reenrollmentBody.ClusterID,
			firstIdentity.ClusterID,
		)
	}
	secondCSR, _ := createHTTPTestAgentCSR(t)
	secondRegistration := performAgentRegistrationRequest(
		t,
		router,
		reenrollmentBody.Token,
		"phase1-reenrollment-consume-0001",
		agentRegistrationRequest{
			CSRPEM:          string(secondCSR),
			AgentVersion:    "phase1-e2e-reenrolled",
			ProtocolVersion: "v1",
		},
	)
	requirePhase1Status(
		t,
		secondRegistration,
		http.StatusCreated,
		"register replacement connection",
	)
	var secondIdentity agentRegistrationResponse
	decodePhase1Response(t, secondRegistration, &secondIdentity)
	if secondIdentity.ClusterID != firstIdentity.ClusterID ||
		secondIdentity.AgentID == firstIdentity.AgentID {
		t.Fatalf(
			"replacement identity = %+v, first = %+v",
			secondIdentity,
			firstIdentity,
		)
	}

	/*
	 * A non-administrator walks into the routes it cannot pass.
	 *
	 * Everything above this point is done by the global administrator and is
	 * therefore always allowed, so the vocabulary check further down had never
	 * once seen a denial — which is precisely how permission names came to be
	 * written into `action` with nothing noticing. One refusal per scope kind
	 * exercises the middleware's global, Project and Cluster audit paths, plus
	 * the audit query's own refusal, and puts their actions and target types in
	 * front of the check that follows.
	 *
	 * Run before the RoleBinding and the user are deleted: the viewer needs a
	 * live session and a binding that makes the Project and Cluster visible, so
	 * that authorization refuses on the permission rather than on visibility.
	 */
	viewerLogin := httptest.NewRecorder()
	viewerLoginRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/auth/login",
		strings.NewReader(
			`{"username":"phase1-user","password":"`+userPassword+`"}`,
		),
	)
	viewerLoginRequest.Header.Set("Content-Type", "application/json")
	viewerLoginRequest.RemoteAddr = "192.0.2.81:1234"
	router.ServeHTTP(viewerLogin, viewerLoginRequest)
	requirePhase1Status(t, viewerLogin, http.StatusOK, "login as project viewer")
	viewerSession := findCookie(
		t,
		viewerLogin.Result().Cookies(),
		sessionCookieName,
	)
	viewerCSRF := findCookie(
		t,
		viewerLogin.Result().Cookies(),
		csrfCookieName,
	)

	refusals := []struct {
		operation string
		method    string
		path      string
		body      string
		csrfToken string
		action    string
	}{
		{
			operation: "create user as viewer",
			method:    http.MethodPost,
			path:      "/api/v1/users",
			body: `{"username":"phase1-intruder","display_name":"Intruder",` +
				`"password":"` + userPassword + `"}`,
			csrfToken: viewerCSRF.Value,
			action:    string(rbac.PermissionUserManage),
		},
		{
			operation: "update project as viewer",
			method:    http.MethodPut,
			path:      "/api/v1/projects/" + project.ID,
			body:      `{"name":"Refused Project","status":"active","confirm":true}`,
			csrfToken: viewerCSRF.Value,
			action:    string(rbac.PermissionProjectManage),
		},
		{
			operation: "update cluster as viewer",
			method:    http.MethodPut,
			path:      "/api/v1/clusters/" + firstIdentity.ClusterID,
			body:      `{"name":"Refused Cluster","confirm":true}`,
			csrfToken: viewerCSRF.Value,
			action:    string(rbac.PermissionClusterManage),
		},
		{
			operation: "query audit events as viewer",
			method:    http.MethodGet,
			path:      "/api/v1/audit-events",
			action:    string(rbac.PermissionAuditRead),
		},
	}
	refusedActions := make([]string, 0, len(refusals))
	for _, refusal := range refusals {
		refused := phase1APIRequest(
			router,
			refusal.method,
			refusal.path,
			refusal.body,
			viewerSession,
			refusal.csrfToken,
			"",
		)
		requirePhase1Status(
			t,
			refused,
			http.StatusForbidden,
			refusal.operation,
		)
		refusedActions = append(refusedActions, refusal.action)
	}

	// Refusing the request is only half of it: the refusal has to be on record,
	// under the permission that produced it.
	var recordedRefusals int
	if err := pool.QueryRow(ctx, `
SELECT count(DISTINCT action)
FROM audit_events
WHERE result = 'denied'
  AND action = ANY($1)
`, refusedActions).Scan(&recordedRefusals); err != nil {
		t.Fatal(err)
	}
	if recordedRefusals != len(refusedActions) {
		t.Fatalf(
			"recorded denial actions = %d, want %d (%v)",
			recordedRefusals,
			len(refusedActions),
			refusedActions,
		)
	}

	deletedBinding := phase1APIRequest(
		router,
		http.MethodDelete,
		"/api/v1/role-bindings/"+binding.ID,
		`{"confirm":true}`,
		sessionCookie,
		csrfCookie.Value,
		"",
	)
	requirePhase1Status(
		t,
		deletedBinding,
		http.StatusOK,
		"delete role binding",
	)
	deletedCluster := phase1APIRequest(
		router,
		http.MethodDelete,
		"/api/v1/clusters/"+firstIdentity.ClusterID,
		`{"confirm":true}`,
		sessionCookie,
		csrfCookie.Value,
		"",
	)
	requirePhase1Status(t, deletedCluster, http.StatusOK, "delete cluster")
	deletedProject := phase1APIRequest(
		router,
		http.MethodDelete,
		"/api/v1/projects/"+project.ID,
		`{"confirm":true}`,
		sessionCookie,
		csrfCookie.Value,
		"",
	)
	requirePhase1Status(t, deletedProject, http.StatusOK, "delete project")
	deletedTenant := phase1APIRequest(
		router,
		http.MethodDelete,
		"/api/v1/tenants/"+tenant.ID,
		`{"confirm":true}`,
		sessionCookie,
		csrfCookie.Value,
		"",
	)
	requirePhase1Status(t, deletedTenant, http.StatusOK, "delete tenant")
	deletedUser := phase1APIRequest(
		router,
		http.MethodDelete,
		"/api/v1/users/"+user.ID,
		`{"confirm":true}`,
		sessionCookie,
		csrfCookie.Value,
		"",
	)
	requirePhase1Status(t, deletedUser, http.StatusOK, "delete user")

	auditEvents := phase1APIRequest(
		router,
		http.MethodGet,
		"/api/v1/audit-events?cluster_id="+firstIdentity.ClusterID+"&limit=100",
		"",
		sessionCookie,
		"",
		"",
	)
	requirePhase1Status(t, auditEvents, http.StatusOK, "query audit events")
	var auditPage struct {
		Events []auditEventResponse `json:"audit_events"`
	}
	decodePhase1Response(t, auditEvents, &auditPage)
	if len(auditPage.Events) == 0 {
		t.Fatal("Cluster-scoped audit query returned no events")
	}

	/*
	 * Every action this run actually wrote must be in the published vocabulary.
	 *
	 * Several action names are embedded in the store's SQL rather than passed as
	 * Go values, so nothing at compile time ties them to `auditaction`. The
	 * Console builds its filter from that vocabulary and the filter is an exact
	 * match, so an action missing from it is an action nobody can filter by —
	 * and the omission is invisible until someone goes looking for those events.
	 * This is the check that makes adding an action without publishing it fail.
	 */
	var recordedActions []string
	rows, err := pool.Query(ctx, "SELECT DISTINCT action FROM audit_events ORDER BY action")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var action string
		if err := rows.Scan(&action); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		recordedActions = append(recordedActions, action)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(recordedActions) == 0 {
		t.Fatal("no audit actions were recorded, so the vocabulary check proves nothing")
	}
	for _, action := range recordedActions {
		if !auditaction.Known(action) {
			t.Errorf(
				"audit action %q is written but missing from auditaction.All(); "+
					"the Console cannot offer it as a filter",
				action,
			)
		}
	}

	// `target_type` is the second closed vocabulary behind the second exact-match
	// filter, and it drifts the same way.
	var recordedTargetTypes []string
	targetRows, err := pool.Query(
		ctx,
		"SELECT DISTINCT target_type FROM audit_events ORDER BY target_type",
	)
	if err != nil {
		t.Fatal(err)
	}
	for targetRows.Next() {
		var targetType string
		if err := targetRows.Scan(&targetType); err != nil {
			targetRows.Close()
			t.Fatal(err)
		}
		recordedTargetTypes = append(recordedTargetTypes, targetType)
	}
	targetRows.Close()
	if err := targetRows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(recordedTargetTypes) == 0 {
		t.Fatal("no audit target types were recorded, so the vocabulary check proves nothing")
	}
	for _, targetType := range recordedTargetTypes {
		if !auditaction.KnownTargetType(targetType) {
			t.Errorf(
				"audit target type %q is written but missing from "+
					"auditaction.TargetTypes()",
				targetType,
			)
		}
	}

	// The published vocabulary must also be reachable, and describe itself.
	actionList := phase1APIRequest(
		router,
		http.MethodGet,
		"/api/v1/audit-events/actions",
		"",
		sessionCookie,
		"",
		"",
	)
	requirePhase1Status(t, actionList, http.StatusOK, "list audit actions")
	var actionPage struct {
		AuditActions    []auditActionResponse `json:"audit_actions"`
		AuditTargetType []string              `json:"audit_target_types"`
	}
	decodePhase1Response(t, actionList, &actionPage)
	if len(actionPage.AuditActions) != len(auditaction.All()) {
		t.Fatalf(
			"published audit actions = %d, want %d",
			len(actionPage.AuditActions),
			len(auditaction.All()),
		)
	}
	for _, action := range actionPage.AuditActions {
		if action.Name == "" || action.Group == "" {
			t.Fatalf("published audit action is incomplete: %+v", action)
		}
	}
	if len(actionPage.AuditTargetType) != len(auditaction.TargetTypes()) {
		t.Fatalf(
			"published audit target types = %d, want %d",
			len(actionPage.AuditTargetType),
			len(auditaction.TargetTypes()),
		)
	}

	var clusterStatus, userStatus, tenantStatus, projectStatus string
	var agentCount, activeAgentCount, credentialCount, activeCredentialCount int
	if err := pool.QueryRow(ctx, `
SELECT
    (SELECT status FROM clusters WHERE id = $1),
    (SELECT status FROM users WHERE id = $2),
    (SELECT status FROM tenants WHERE id = $3),
    (SELECT status FROM projects WHERE id = $4),
    (SELECT count(*) FROM agents WHERE cluster_id = $1),
    (
        SELECT count(*) FROM agents
        WHERE cluster_id = $1 AND lifecycle_status <> 'revoked'
    ),
    (SELECT count(*) FROM agent_credentials WHERE cluster_id = $1),
    (
        SELECT count(*) FROM agent_credentials
        WHERE cluster_id = $1 AND revoked_at IS NULL
    )
`, firstIdentity.ClusterID, user.ID, tenant.ID, project.ID).Scan(
		&clusterStatus,
		&userStatus,
		&tenantStatus,
		&projectStatus,
		&agentCount,
		&activeAgentCount,
		&credentialCount,
		&activeCredentialCount,
	); err != nil {
		t.Fatal(err)
	}
	if clusterStatus != "revoked" ||
		userStatus != "disabled" ||
		tenantStatus != "suspended" ||
		projectStatus != "suspended" ||
		agentCount != 2 ||
		activeAgentCount != 0 ||
		credentialCount != 2 ||
		activeCredentialCount != 0 {
		t.Fatalf(
			"final Phase 1 state cluster/user/tenant/project/agents/active/credentials/active = "+
				"%s/%s/%s/%s/%d/%d/%d/%d",
			clusterStatus,
			userStatus,
			tenantStatus,
			projectStatus,
			agentCount,
			activeAgentCount,
			credentialCount,
			activeCredentialCount,
		)
	}
	if admin.ID == "" {
		t.Fatal("initial administrator was not created")
	}
}

func phase1APIRequest(
	handler http.Handler,
	method string,
	path string,
	body string,
	sessionCookie *http.Cookie,
	csrfToken string,
	idempotencyKey string,
) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if sessionCookie != nil {
		request.AddCookie(sessionCookie)
	}
	if csrfToken != "" {
		request.Header.Set(csrfHeaderName, csrfToken)
	}
	if idempotencyKey != "" {
		request.Header.Set(idempotencyKeyHeaderName, idempotencyKey)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	handler.ServeHTTP(response, request)
	return response
}

func phase1InstallationToken(t *testing.T, command string) string {
	t.Helper()
	const prefix = "Authorization: Bearer "
	start := strings.Index(command, prefix)
	if start < 0 {
		t.Fatalf("installation command has no Bearer token: %s", command)
	}
	start += len(prefix)
	end := strings.Index(command[start:], "'")
	if end < 0 {
		t.Fatalf("installation command has an invalid Bearer token: %s", command)
	}
	return command[start : start+end]
}

func requirePhase1Status(
	t *testing.T,
	response *httptest.ResponseRecorder,
	want int,
	operation string,
) {
	t.Helper()
	if response.Code != want {
		t.Fatalf(
			"%s status = %d, want %d: %s",
			operation,
			response.Code,
			want,
			response.Body,
		)
	}
}

func decodePhase1Response(
	t *testing.T,
	response *httptest.ResponseRecorder,
	target any,
) {
	t.Helper()
	if err := decodeSuccessResponse(response, target); err != nil {
		t.Fatalf("decode response %q: %v", response.Body.String(), err)
	}
}

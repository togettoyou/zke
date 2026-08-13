package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/togettoyou/zke/pkg/server/agentconn"
	"github.com/togettoyou/zke/pkg/server/agentstatus"
	"github.com/togettoyou/zke/pkg/server/audit"
	"github.com/togettoyou/zke/pkg/server/auth"
	"github.com/togettoyou/zke/pkg/server/rbac"
	"github.com/togettoyou/zke/pkg/server/resourcemanagement"
	"github.com/togettoyou/zke/pkg/server/store"
)

func TestResourceManagementHTTPFlow(t *testing.T) {
	databaseURL := requireHTTPTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := openHTTPTestDatabase(t, ctx, databaseURL)
	applyMigrations(t, ctx, pool)

	password := []byte("a sufficiently long resource administrator passphrase")
	admin, err := auth.CreateFirstGlobalAdministrator(ctx, store.NewAuthStore(pool), auth.FirstGlobalAdministratorInput{
		Username:    "resource-admin",
		DisplayName: "Resource Administrator",
		Password:    password,
	})
	if err != nil {
		t.Fatal(err)
	}
	authService := auth.NewService(store.NewAuthStore(pool), auth.ServiceConfig{
		SessionIdleTimeout:          30 * time.Minute,
		SessionAbsoluteTimeout:      8 * time.Hour,
		MaxConcurrentPasswordChecks: 1,
	})
	adminLogin, err := authService.Login(ctx, auth.LoginInput{
		Username:  admin.Username,
		Password:  password,
		RequestID: "request-resource-admin-login",
		Now:       time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}

	connections := &fakeAgentConnections{
		statuses: make(map[string]agentconn.ConnectionStatus),
	}
	auditService := audit.NewService(store.NewAuditStore(pool), rbac.NewService(store.NewRBACStore(pool)))
	router := New(
		discardLogger(),
		Dependencies{
			ReadinessCheck: pool.Ping,
			AuthService:    authService,
			AuditService:   auditService,
			RBACService:    rbac.NewService(store.NewRBACStore(pool)),
			ResourceManagementService: resourcemanagement.NewService(
				store.NewResourceManagementStore(pool),
				rbac.NewService(store.NewRBACStore(pool)),
			),
			AgentStatusService: agentstatus.NewService(
				store.NewAgentStatusStore(pool),
				connections,
				connections,
				7*24*time.Hour,
			),
		},
		Config{Authentication: defaultAuthenticationTestConfig()},
	)

	missingKey := resourceAPIRequest(
		router,
		http.MethodPost,
		"/api/v1/tenants",
		`{"name":"Missing Key"}`,
		adminLogin,
		true,
		"",
	)
	if missingKey.Code != http.StatusBadRequest {
		t.Fatalf("missing idempotency key status = %d: %s", missingKey.Code, missingKey.Body)
	}
	assertErrorCode(t, missingKey, "invalid_request")

	tenantKey := "tenant-create-key-0001"
	createdTenant := resourceAPIRequest(
		router,
		http.MethodPost,
		"/api/v1/tenants",
		`{"name":"Primary Tenant"}`,
		adminLogin,
		true,
		tenantKey,
	)
	if createdTenant.Code != http.StatusCreated {
		t.Fatalf("create tenant status = %d: %s", createdTenant.Code, createdTenant.Body)
	}
	var tenant tenantResponse
	if err := decodeSuccessResponse(createdTenant, &tenant); err != nil {
		t.Fatal(err)
	}
	if tenant.ID == "" || tenant.Name != "Primary Tenant" ||
		tenant.Status != "active" || tenant.Replayed {
		t.Fatalf("unexpected tenant response: %+v", tenant)
	}
	assertUTC8Time(t, "tenant created_at", tenant.CreatedAt)
	assertUTC8Time(t, "tenant updated_at", tenant.UpdatedAt)

	replayedTenant := resourceAPIRequest(
		router,
		http.MethodPost,
		"/api/v1/tenants",
		`{"name":"Primary Tenant"}`,
		adminLogin,
		true,
		tenantKey,
	)
	if replayedTenant.Code != http.StatusOK {
		t.Fatalf("replay tenant status = %d: %s", replayedTenant.Code, replayedTenant.Body)
	}
	var tenantReplay tenantResponse
	if err := decodeSuccessResponse(replayedTenant, &tenantReplay); err != nil {
		t.Fatal(err)
	}
	if !tenantReplay.Replayed || tenantReplay.ID != tenant.ID {
		t.Fatalf("unexpected tenant replay: %+v", tenantReplay)
	}
	conflictingTenant := resourceAPIRequest(
		router,
		http.MethodPost,
		"/api/v1/tenants",
		`{"name":"Different Tenant"}`,
		adminLogin,
		true,
		tenantKey,
	)
	if conflictingTenant.Code != http.StatusConflict {
		t.Fatalf("tenant conflict status = %d: %s", conflictingTenant.Code, conflictingTenant.Body)
	}
	assertErrorCode(t, conflictingTenant, "idempotency_conflict")

	tenantDetail := resourceAPIRequest(
		router, http.MethodGet, "/api/v1/tenants/"+tenant.ID, "",
		adminLogin, false, "",
	)
	if tenantDetail.Code != http.StatusOK {
		t.Fatalf("get tenant status = %d: %s", tenantDetail.Code, tenantDetail.Body)
	}
	updatedTenant := resourceAPIRequest(
		router,
		http.MethodPut,
		"/api/v1/tenants/"+tenant.ID,
		`{"name":"Updated Primary Tenant","status":"active","confirm":true}`,
		adminLogin,
		true,
		"",
	)
	if updatedTenant.Code != http.StatusOK {
		t.Fatalf("update tenant status = %d: %s", updatedTenant.Code, updatedTenant.Body)
	}
	if err := decodeSuccessResponse(updatedTenant, &tenant); err != nil {
		t.Fatal(err)
	}
	if tenant.Name != "Updated Primary Tenant" {
		t.Fatalf("updated tenant name = %q", tenant.Name)
	}

	tenantList := resourceAPIRequest(
		router,
		http.MethodGet,
		"/api/v1/tenants",
		"",
		adminLogin,
		false,
		"",
	)
	if tenantList.Code != http.StatusOK {
		t.Fatalf("list tenants status = %d: %s", tenantList.Code, tenantList.Body)
	}
	var listedTenants struct {
		Tenants []tenantResponse `json:"tenants"`
	}
	if err := decodeSuccessResponse(tenantList, &listedTenants); err != nil {
		t.Fatal(err)
	}
	if len(listedTenants.Tenants) != 1 || listedTenants.Tenants[0].ID != tenant.ID {
		t.Fatalf("unexpected tenants: %+v", listedTenants.Tenants)
	}

	projectPath := "/api/v1/tenants/" + tenant.ID + "/projects"
	projectKey := "project-create-key-0001"
	createdProject := resourceAPIRequest(
		router,
		http.MethodPost,
		projectPath,
		`{"name":"Primary Project"}`,
		adminLogin,
		true,
		projectKey,
	)
	if createdProject.Code != http.StatusCreated {
		t.Fatalf("create project status = %d: %s", createdProject.Code, createdProject.Body)
	}
	var project projectResponse
	if err := decodeSuccessResponse(createdProject, &project); err != nil {
		t.Fatal(err)
	}
	if project.ID == "" || project.TenantID != tenant.ID ||
		project.Name != "Primary Project" || project.Replayed {
		t.Fatalf("unexpected project response: %+v", project)
	}
	assertUTC8Time(t, "project created_at", project.CreatedAt)
	assertUTC8Time(t, "project updated_at", project.UpdatedAt)

	replayedProject := resourceAPIRequest(
		router,
		http.MethodPost,
		projectPath,
		`{"name":"Primary Project"}`,
		adminLogin,
		true,
		projectKey,
	)
	if replayedProject.Code != http.StatusOK {
		t.Fatalf("replay project status = %d: %s", replayedProject.Code, replayedProject.Body)
	}
	var projectReplay projectResponse
	if err := decodeSuccessResponse(replayedProject, &projectReplay); err != nil {
		t.Fatal(err)
	}
	if !projectReplay.Replayed || projectReplay.ID != project.ID {
		t.Fatalf("unexpected project replay: %+v", projectReplay)
	}
	conflictingProject := resourceAPIRequest(
		router,
		http.MethodPost,
		projectPath,
		`{"name":"Different Project"}`,
		adminLogin,
		true,
		projectKey,
	)
	if conflictingProject.Code != http.StatusConflict {
		t.Fatalf("project conflict status = %d: %s", conflictingProject.Code, conflictingProject.Body)
	}
	assertErrorCode(t, conflictingProject, "idempotency_conflict")

	projectDetail := resourceAPIRequest(
		router, http.MethodGet, "/api/v1/projects/"+project.ID, "",
		adminLogin, false, "",
	)
	if projectDetail.Code != http.StatusOK {
		t.Fatalf("get project status = %d: %s", projectDetail.Code, projectDetail.Body)
	}
	updatedProject := resourceAPIRequest(
		router,
		http.MethodPut,
		"/api/v1/projects/"+project.ID,
		`{"name":"Updated Primary Project","status":"active","confirm":true}`,
		adminLogin,
		true,
		"",
	)
	if updatedProject.Code != http.StatusOK {
		t.Fatalf("update project status = %d: %s", updatedProject.Code, updatedProject.Body)
	}
	if err := decodeSuccessResponse(updatedProject, &project); err != nil {
		t.Fatal(err)
	}
	if project.Name != "Updated Primary Project" {
		t.Fatalf("updated project name = %q", project.Name)
	}

	var clusterID, agentID string
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := pool.QueryRow(ctx, `
INSERT INTO clusters (
    id, tenant_id, project_id, name, status, last_seen_at
)
VALUES (gen_random_uuid(), $1, $2, 'Primary Cluster', 'active', $3)
RETURNING id::text
`, tenant.ID, project.ID, now).Scan(&clusterID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
INSERT INTO agents (
    id, tenant_id, project_id, cluster_id, version, protocol_version,
    lifecycle_status, health_status, active_credential_serial, last_seen_at
)
VALUES (
    gen_random_uuid(), $1, $2, $3, 'development', 'v1', 'active',
    'healthy', 'resource-serial', $4
)
RETURNING id::text
`, tenant.ID, project.ID, clusterID, now).Scan(&agentID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO agent_credentials (
    id, tenant_id, project_id, cluster_id, agent_id, serial,
    csr_fingerprint, certificate_pem, expires_at
)
VALUES (
    gen_random_uuid(), $1, $2, $3, $4, 'resource-serial',
    decode('01', 'hex'), 'certificate', $5
)
`, tenant.ID, project.ID, clusterID, agentID, now.Add(30*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	connections.setStatus(agentID, agentconn.ConnectionStatus{
		State:           agentconn.ConnectionStateOnline,
		ConnectionID:    "resource-connection",
		ConnectedAt:     now.Add(-time.Minute),
		LastHeartbeatAt: now,
	})

	clusterList := resourceAPIRequest(
		router,
		http.MethodGet,
		"/api/v1/projects/"+project.ID+"/clusters",
		"",
		adminLogin,
		false,
		"",
	)
	if clusterList.Code != http.StatusOK {
		t.Fatalf("list clusters status = %d: %s", clusterList.Code, clusterList.Body)
	}
	var listedClusters struct {
		Clusters []agentStatusResponse `json:"clusters"`
	}
	if err := decodeSuccessResponse(clusterList, &listedClusters); err != nil {
		t.Fatal(err)
	}
	if len(listedClusters.Clusters) != 1 ||
		listedClusters.Clusters[0].ID != clusterID {
		t.Fatalf("unexpected clusters: %+v", listedClusters.Clusters)
	}
	assertUTC8TimePointer(t, "cluster last_seen_at", listedClusters.Clusters[0].Connection.LastSeenAt)
	assertUTC8Time(t, "cluster created_at", listedClusters.Clusters[0].CreatedAt)

	clusterDetail := resourceAPIRequest(
		router,
		http.MethodGet,
		"/api/v1/clusters/"+clusterID,
		"",
		adminLogin,
		false,
		"",
	)
	if clusterDetail.Code != http.StatusOK {
		t.Fatalf("cluster detail status = %d: %s", clusterDetail.Code, clusterDetail.Body)
	}
	var cluster agentStatusResponse
	if err := decodeSuccessResponse(clusterDetail, &cluster); err != nil {
		t.Fatal(err)
	}
	if cluster.ID != clusterID || cluster.ProjectID != project.ID ||
		cluster.Connection.Status != agentconn.ConnectionStateOnline ||
		cluster.Connection.ConnectionID != "resource-connection" {
		t.Fatalf("unexpected cluster detail: %+v", cluster)
	}
	assertUTC8Time(t, "Cluster certificate_expires_at", cluster.Connection.CertificateExpiresAt)

	updatedCluster := resourceAPIRequest(
		router,
		http.MethodPut,
		"/api/v1/clusters/"+clusterID,
		`{"name":"Updated Primary Cluster","status":"active"}`,
		adminLogin,
		true,
		"",
	)
	if updatedCluster.Code != http.StatusOK {
		t.Fatalf("update cluster status = %d: %s", updatedCluster.Code, updatedCluster.Body)
	}
	var updatedClusterBody clusterResponse
	if err := decodeSuccessResponse(updatedCluster, &updatedClusterBody); err != nil {
		t.Fatal(err)
	}
	if updatedClusterBody.Name != "Updated Primary Cluster" {
		t.Fatalf("updated cluster name = %q", updatedClusterBody.Name)
	}

	var emptyClusterID string
	if err := pool.QueryRow(ctx, `
INSERT INTO clusters (id, tenant_id, project_id, name, status)
VALUES (gen_random_uuid(), $1, $2, 'Pending Cluster', 'pending')
RETURNING id::text
`, tenant.ID, project.ID).Scan(&emptyClusterID); err != nil {
		t.Fatal(err)
	}
	missingAgent := resourceAPIRequest(
		router,
		http.MethodGet,
		"/api/v1/clusters/"+emptyClusterID,
		"",
		adminLogin,
		false,
		"",
	)
	if missingAgent.Code != http.StatusNotFound {
		t.Fatalf("missing Agent status = %d: %s", missingAgent.Code, missingAgent.Body)
	}
	assertErrorCode(t, missingAgent, "not_found")

	viewerLogin, otherTenantID, otherProjectID, otherClusterID :=
		createResourceViewerScope(t, ctx, pool, authService, password, tenant.ID, project.ID)
	viewerTenants := resourceAPIRequest(
		router,
		http.MethodGet,
		"/api/v1/tenants",
		"",
		viewerLogin,
		false,
		"",
	)
	if viewerTenants.Code != http.StatusOK {
		t.Fatalf("viewer tenant list status = %d: %s", viewerTenants.Code, viewerTenants.Body)
	}
	listedTenants.Tenants = nil
	if err := decodeSuccessResponse(viewerTenants, &listedTenants); err != nil {
		t.Fatal(err)
	}
	if len(listedTenants.Tenants) != 1 || listedTenants.Tenants[0].ID != tenant.ID {
		t.Fatalf("viewer tenants crossed scope: %+v", listedTenants.Tenants)
	}

	viewerProjects := resourceAPIRequest(
		router,
		http.MethodGet,
		projectPath,
		"",
		viewerLogin,
		false,
		"",
	)
	var listedProjects struct {
		Projects []projectResponse `json:"projects"`
	}
	if viewerProjects.Code != http.StatusOK {
		t.Fatalf("viewer project list status = %d: %s", viewerProjects.Code, viewerProjects.Body)
	}
	if err := decodeSuccessResponse(viewerProjects, &listedProjects); err != nil {
		t.Fatal(err)
	}
	if len(listedProjects.Projects) != 1 || listedProjects.Projects[0].ID != project.ID {
		t.Fatalf("viewer projects crossed scope: %+v", listedProjects.Projects)
	}
	hiddenProjects := resourceAPIRequest(
		router,
		http.MethodGet,
		"/api/v1/tenants/"+otherTenantID+"/projects",
		"",
		viewerLogin,
		false,
		"",
	)
	if hiddenProjects.Code != http.StatusOK {
		t.Fatalf("hidden project list status = %d: %s", hiddenProjects.Code, hiddenProjects.Body)
	}
	listedProjects.Projects = nil
	if err := decodeSuccessResponse(hiddenProjects, &listedProjects); err != nil {
		t.Fatal(err)
	}
	if len(listedProjects.Projects) != 0 {
		t.Fatalf("viewer saw hidden projects: %+v", listedProjects.Projects)
	}

	viewerTenantCreate := resourceAPIRequest(
		router,
		http.MethodPost,
		"/api/v1/tenants",
		`{"name":"Denied Tenant"}`,
		viewerLogin,
		true,
		"viewer-tenant-key-0001",
	)
	if viewerTenantCreate.Code != http.StatusForbidden {
		t.Fatalf("viewer tenant create status = %d", viewerTenantCreate.Code)
	}
	viewerProjectCreate := resourceAPIRequest(
		router,
		http.MethodPost,
		projectPath,
		`{"name":"Denied Project"}`,
		viewerLogin,
		true,
		"viewer-project-key-0001",
	)
	if viewerProjectCreate.Code != http.StatusForbidden {
		t.Fatalf("viewer project create status = %d", viewerProjectCreate.Code)
	}
	crossCluster := resourceAPIRequest(
		router,
		http.MethodGet,
		"/api/v1/clusters/"+otherClusterID,
		"",
		viewerLogin,
		false,
		"",
	)
	if crossCluster.Code != http.StatusForbidden {
		t.Fatalf("cross-scope cluster status = %d", crossCluster.Code)
	}

	var tenantCount, tenantRequests, tenantSucceeded int
	if err := pool.QueryRow(ctx, `
SELECT
    (SELECT count(*) FROM tenants WHERE id = $1),
    (SELECT count(*) FROM tenant_creation_requests WHERE tenant_id = $1),
    (
        SELECT count(*)
        FROM audit_events
        WHERE action = 'tenant.create'
          AND target_id = $1
          AND result = 'succeeded'
    )
`, tenant.ID).Scan(&tenantCount, &tenantRequests, &tenantSucceeded); err != nil {
		t.Fatal(err)
	}
	if tenantCount != 1 || tenantRequests != 1 || tenantSucceeded != 1 {
		t.Fatalf(
			"tenant persistence count/resource request/audit = %d/%d/%d",
			tenantCount,
			tenantRequests,
			tenantSucceeded,
		)
	}
	var projectCount, projectRequests, projectSucceeded int
	if err := pool.QueryRow(ctx, `
SELECT
    (SELECT count(*) FROM projects WHERE id = $1),
    (SELECT count(*) FROM project_creation_requests WHERE project_id = $1),
    (
        SELECT count(*)
        FROM audit_events
        WHERE action = 'project.create'
          AND target_id = $1
          AND result = 'succeeded'
    )
`, project.ID).Scan(&projectCount, &projectRequests, &projectSucceeded); err != nil {
		t.Fatal(err)
	}
	if projectCount != 1 || projectRequests != 1 || projectSucceeded != 1 {
		t.Fatalf(
			"project persistence count/resource request/audit = %d/%d/%d",
			projectCount,
			projectRequests,
			projectSucceeded,
		)
	}
	var tenantFailed, tenantDenied, projectDenied, clusterDenied int
	if err := pool.QueryRow(ctx, `
SELECT
    count(*) FILTER (
        WHERE action = 'tenant.create' AND result = 'failed'
    ),
    count(*) FILTER (
        WHERE action = 'tenant.create' AND result = 'denied'
    ),
    count(*) FILTER (
        WHERE action = 'project.create' AND result = 'denied'
    ),
    count(*) FILTER (
        WHERE action = 'cluster.read'
          AND result = 'denied'
          AND target_id = $1
    )
FROM audit_events
`, otherClusterID).Scan(
		&tenantFailed,
		&tenantDenied,
		&projectDenied,
		&clusterDenied,
	); err != nil {
		t.Fatal(err)
	}
	if tenantFailed != 1 || tenantDenied != 1 ||
		projectDenied != 1 || clusterDenied != 1 {
		t.Fatalf(
			"resource failed/denied audits = %d/%d/%d/%d, want 1/1/1/1",
			tenantFailed,
			tenantDenied,
			projectDenied,
			clusterDenied,
		)
	}
	if otherProjectID == "" {
		t.Fatal("resource viewer fixture did not create the hidden project")
	}

	deletedCluster := resourceAPIRequest(
		router,
		http.MethodDelete,
		"/api/v1/clusters/"+clusterID,
		`{"confirm":true}`,
		adminLogin,
		true,
		"",
	)
	if deletedCluster.Code != http.StatusOK {
		t.Fatalf("delete cluster status = %d: %s", deletedCluster.Code, deletedCluster.Body)
	}
	var deletedClusterBody clusterResponse
	if err := decodeSuccessResponse(deletedCluster, &deletedClusterBody); err != nil {
		t.Fatal(err)
	}
	// The response carries the Cluster as it was immediately before deletion,
	// which is the last chance anything has to report it.
	if deletedClusterBody.Status == "" {
		t.Fatalf("deleted cluster response = %+v, want the deleted cluster", deletedClusterBody)
	}
	var clusterRows, agentRows, credentialRows int
	if err := pool.QueryRow(ctx, `
SELECT
    (SELECT count(*) FROM clusters WHERE id = $1),
    (SELECT count(*) FROM agents WHERE cluster_id = $1),
    (SELECT count(*) FROM agent_credentials WHERE cluster_id = $1)
`, clusterID).Scan(&clusterRows, &agentRows, &credentialRows); err != nil {
		t.Fatal(err)
	}
	if clusterRows != 0 || agentRows != 0 || credentialRows != 0 {
		t.Fatalf(
			"after cluster deletion cluster/agent/credential rows = %d/%d/%d, want 0/0/0",
			clusterRows,
			agentRows,
			credentialRows,
		)
	}

	deletedProject := resourceAPIRequest(
		router,
		http.MethodDelete,
		"/api/v1/projects/"+project.ID,
		`{"confirm":true}`,
		adminLogin,
		true,
		"",
	)
	if deletedProject.Code != http.StatusOK {
		t.Fatalf("delete project status = %d: %s", deletedProject.Code, deletedProject.Body)
	}
	var deletedProjectBody projectResponse
	if err := decodeSuccessResponse(deletedProject, &deletedProjectBody); err != nil {
		t.Fatal(err)
	}
	if deletedProjectBody.ID != project.ID {
		t.Fatalf("delete project returned %+v, want the deleted project", deletedProjectBody)
	}

	deletedTenant := resourceAPIRequest(
		router,
		http.MethodDelete,
		"/api/v1/tenants/"+tenant.ID,
		`{"confirm":true}`,
		adminLogin,
		true,
		"",
	)
	if deletedTenant.Code != http.StatusOK {
		t.Fatalf("delete tenant status = %d: %s", deletedTenant.Code, deletedTenant.Body)
	}
	var deletedTenantBody tenantResponse
	if err := decodeSuccessResponse(deletedTenant, &deletedTenantBody); err != nil {
		t.Fatal(err)
	}
	if deletedTenantBody.ID != tenant.ID {
		t.Fatalf("delete tenant returned %+v, want the deleted tenant", deletedTenantBody)
	}

	// Nothing is left behind, and the audit trail that records the removal is.
	var remaining, auditRows int
	if err := pool.QueryRow(ctx, `
SELECT
    (SELECT count(*) FROM tenants WHERE id = $1)
        + (SELECT count(*) FROM projects WHERE tenant_id = $1)
        + (SELECT count(*) FROM clusters WHERE tenant_id = $1),
    (SELECT count(*) FROM audit_events WHERE tenant_id = $1)
`, tenant.ID).Scan(&remaining, &auditRows); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("rows left under the deleted tenant = %d, want 0", remaining)
	}
	if auditRows == 0 {
		t.Fatal("the deleted tenant's audit events were removed with it")
	}
}

func createResourceViewerScope(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	authService *auth.Service,
	password []byte,
	allowedTenantID string,
	allowedProjectID string,
) (auth.LoginResult, string, string, string) {
	t.Helper()
	passwordHash, err := auth.HashPassword(password, auth.DefaultPasswordParams())
	if err != nil {
		t.Fatal(err)
	}
	var viewerID, otherTenantID, otherProjectID, otherClusterID string
	if err := pool.QueryRow(ctx, `
INSERT INTO users (
    id, username_normalized, display_name, password_hash, status,
    password_changed_at
)
VALUES (
    gen_random_uuid(), 'resource-viewer', 'Resource Viewer', $1, 'active', now()
)
RETURNING id::text
`, passwordHash).Scan(&viewerID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
INSERT INTO role_bindings (
    id, subject_id, role, scope_type, tenant_id, project_id
)
VALUES (gen_random_uuid(), $1, 'viewer', 'project', $2, $3)
RETURNING id::text
`, viewerID, allowedTenantID, allowedProjectID).Scan(new(string)); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
INSERT INTO tenants (id, name, status)
VALUES (gen_random_uuid(), 'Hidden Tenant', 'active')
RETURNING id::text
`).Scan(&otherTenantID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
INSERT INTO projects (id, tenant_id, name, status)
VALUES (gen_random_uuid(), $1, 'Hidden Project', 'active')
RETURNING id::text
`, otherTenantID).Scan(&otherProjectID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
INSERT INTO clusters (id, tenant_id, project_id, name, status)
VALUES (gen_random_uuid(), $1, $2, 'Hidden Cluster', 'active')
RETURNING id::text
`, otherTenantID, otherProjectID).Scan(&otherClusterID); err != nil {
		t.Fatal(err)
	}
	login, err := authService.Login(ctx, auth.LoginInput{
		Username:  "resource-viewer",
		Password:  password,
		RequestID: "request-resource-viewer-login",
		Now:       time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return login, otherTenantID, otherProjectID, otherClusterID
}

func resourceAPIRequest(
	handler http.Handler,
	method string,
	path string,
	body string,
	login auth.LoginResult,
	withCSRF bool,
	idempotencyKey string,
) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.AddCookie(&http.Cookie{
		Name:  sessionCookieName,
		Value: login.SessionToken,
	})
	if withCSRF {
		request.Header.Set(csrfHeaderName, login.CSRFToken)
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

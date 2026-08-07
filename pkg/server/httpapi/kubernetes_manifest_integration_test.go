package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/server/audit"
	"github.com/togettoyou/zke/pkg/server/auth"
	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
	"github.com/togettoyou/zke/pkg/server/rbac"
	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/shared/kubernetescatalog"
)

// The manifest endpoint's authorization, end to end.
//
// The unit tests decide the rule; this decides that the rule is what the
// deployed Server applies. Everything between a session cookie and the Agent is
// real: the route table, CSRF, `RequireCluster`, the grant middleware reading
// role bindings out of PostgreSQL, the manifest service, and the resource layer
// with its unexported Secret and Namespace flags. Only the Cluster is a stand-in.
//
// It is worth having as a separate layer because the failure it catches is a
// wiring failure, and wiring is exactly what a unit test with a fake grant cannot
// see: a route registered without ResolveClusterManifestGrant would leave every
// caller granting nothing, and every unit test in the tree would still pass.
func TestKubernetesManifestAuthorizationEndToEnd(t *testing.T) {
	databaseURL := requireHTTPTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool := openHTTPTestDatabase(t, ctx, databaseURL)
	applyMigrations(t, ctx, pool)

	var tenantID, projectID, clusterID string
	if err := pool.QueryRow(ctx, `
INSERT INTO tenants (id, name, status)
VALUES (gen_random_uuid(), 'Manifest Tenant', 'active')
RETURNING id::text
`).Scan(&tenantID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
INSERT INTO projects (id, tenant_id, name, status)
VALUES (gen_random_uuid(), $1, 'Manifest Project', 'active')
RETURNING id::text
`, tenantID).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
INSERT INTO clusters (id, tenant_id, project_id, name, status)
VALUES (gen_random_uuid(), $1, $2, 'manifest-cluster', 'pending')
RETURNING id::text
`, tenantID, projectID).Scan(&clusterID); err != nil {
		t.Fatal(err)
	}

	// One role per permission the manifest path can require, each carrying
	// `cluster.read` because that is the route's own floor. Testing them singly is
	// what makes the negative half meaningful: a role holding two permissions
	// cannot tell which one let a document through.
	if _, err := pool.Exec(ctx, `
INSERT INTO roles (id, name, display_name, builtin, permissions)
VALUES
    (gen_random_uuid(), 'manifest-reader', '只读', false,
     ARRAY['cluster.read']),
    (gen_random_uuid(), 'manifest-resource-writer', '通用资源写入', false,
     ARRAY['cluster.read', 'cluster.resource.create', 'cluster.resource.update',
           'cluster.resource.delete']),
    (gen_random_uuid(), 'manifest-namespace-manager', '命名空间管理', false,
     ARRAY['cluster.read', 'cluster.namespace.manage']),
    (gen_random_uuid(), 'manifest-secret-manager', 'Secret 管理', false,
     ARRAY['cluster.read', 'cluster.secret.read', 'cluster.secret.manage']),
    (gen_random_uuid(), 'manifest-rbac-manager', 'RBAC 管理', false,
     ARRAY['cluster.read', 'cluster.rbac.read', 'cluster.rbac.manage']),
    (gen_random_uuid(), 'manifest-full', '全部写入', false,
     ARRAY['cluster.read', 'cluster.resource.create', 'cluster.resource.update',
           'cluster.resource.delete', 'cluster.namespace.manage',
           'cluster.secret.read', 'cluster.secret.manage', 'cluster.rbac.manage'])
`); err != nil {
		t.Fatal(err)
	}

	authService := auth.NewService(store.NewAuthStore(pool), auth.ServiceConfig{
		SessionIdleTimeout:          30 * time.Minute,
		SessionAbsoluteTimeout:      8 * time.Hour,
		MaxConcurrentPasswordChecks: 4,
	})
	rbacService := rbac.NewService(store.NewRBACStore(pool))
	auditService := audit.NewService(store.NewAuditStore(pool), rbacService)

	agent := newManifestTestAgent()
	handler := New(
		discardLogger(),
		Dependencies{
			ReadinessCheck:            pool.Ping,
			AuthService:               authService,
			AuditService:              auditService,
			RBACService:               rbacService,
			KubernetesResourceService: kubernetesresource.NewService(agent),
		},
		Config{
			Authentication: defaultAuthenticationTestConfig(),
			KubernetesManifest: KubernetesManifestHTTPConfig{
				RequestTimeout: 30 * time.Second,
				MaxDocuments:   32,
			},
		},
	)
	router, ok := handler.(*gin.Engine)
	if !ok {
		t.Fatalf("handler type = %T, want *gin.Engine", handler)
	}

	sessions := map[string]*http.Cookie{}
	csrf := map[string]string{}
	for _, role := range []string{
		"manifest-reader",
		"manifest-resource-writer",
		"manifest-namespace-manager",
		"manifest-secret-manager",
		"manifest-rbac-manager",
		"manifest-full",
	} {
		session, token := manifestTestLogin(
			t, ctx, pool, router, role, tenantID, projectID,
		)
		sessions[role] = session
		csrf[role] = token
	}

	submit := func(role string, operation string, manifest string) *httptest.ResponseRecorder {
		t.Helper()
		response := httptest.NewRecorder()
		request := httptest.NewRequest(
			http.MethodPost,
			"/api/v1/clusters/"+clusterID+
				"/kubernetes/manifests/"+operation+"?confirm=true",
			strings.NewReader(manifest),
		)
		request.Header.Set("Content-Type", "application/yaml")
		request.Header.Set(idempotencyKeyHeaderName, "manifest-e2e-idempotency-key")
		request.Header.Set("X-CSRF-Token", csrf[role])
		request.AddCookie(sessions[role])
		router.ServeHTTP(response, request)
		return response
	}

	// Which role may write which family, as a matrix over the real stack.
	//
	// `manifest-reader` holds the route's own permission and nothing else, which
	// is the case that matters most: passing `RequireCluster` must not be enough
	// to write anything at all.
	families := []struct {
		name     string
		document string
	}{
		{"ConfigMap", manifestTestDocument("v1", "ConfigMap", "team-a", "app-config")},
		{"Deployment", manifestTestDocument("apps/v1", "Deployment", "team-a", "web")},
		{"Namespace", manifestTestDocument("v1", "Namespace", "", "team-b")},
		{"Secret", manifestTestDocument("v1", "Secret", "team-a", "app-secret")},
		{"ServiceAccount", manifestTestDocument("v1", "ServiceAccount", "team-a", "app-sa")},
		{
			"Role",
			manifestTestDocument("rbac.authorization.k8s.io/v1", "Role", "team-a", "app-role"),
		},
	}
	permitted := map[string]map[string]bool{
		"manifest-reader": {},
		"manifest-resource-writer": {
			"ConfigMap": true, "Deployment": true,
		},
		"manifest-namespace-manager": {"Namespace": true},
		"manifest-secret-manager":    {"Secret": true},
		"manifest-rbac-manager":      {"ServiceAccount": true, "Role": true},
		"manifest-full": {
			"ConfigMap": true, "Deployment": true, "Namespace": true,
			"Secret": true, "ServiceAccount": true, "Role": true,
		},
	}
	for role, allowedFamilies := range permitted {
		for _, family := range families {
			t.Run(role+"/"+family.name, func(t *testing.T) {
				before := agent.mutations()
				response := submit(role, "apply", family.document)
				after := agent.mutations()

				if allowedFamilies[family.name] {
					if response.Code != http.StatusOK {
						t.Fatalf("status = %d, want 200: %s", response.Code, response.Body)
					}
					if after == before {
						t.Fatal("a permitted document never reached the Cluster")
					}
					return
				}
				if response.Code != http.StatusForbidden {
					t.Fatalf("status = %d, want 403: %s", response.Code, response.Body)
				}
				assertErrorCode(t, response, "forbidden")
				// The refusal is the whole point: nothing may be sent to the
				// Cluster for a document the caller may not write.
				if after != before {
					t.Fatal("a refused document reached the Cluster anyway")
				}
			})
		}
	}

	// A manifest mixing a family the caller may write with one they may not is
	// refused whole. This is the case a per-route permission cannot express, and
	// the one where a partial apply would be worst.
	t.Run("mixed manifest is refused whole", func(t *testing.T) {
		mixed := manifestTestDocument("v1", "ConfigMap", "team-a", "app-config") +
			"---\n" + manifestTestDocument("v1", "Secret", "team-a", "app-secret")
		before := agent.mutations()
		response := submit("manifest-resource-writer", "apply", mixed)
		if response.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403: %s", response.Code, response.Body)
		}
		if agent.mutations() != before {
			t.Fatal("the ConfigMap was written even though the manifest was refused")
		}
	})

	// Deleting answers to `cluster.resource.delete`, and a caller holding only the
	// create and update halves must not get there through a manifest.
	t.Run("delete needs the delete permission", func(t *testing.T) {
		if _, err := pool.Exec(ctx, `
UPDATE roles SET permissions = ARRAY['cluster.read', 'cluster.resource.create',
                                     'cluster.resource.update']
WHERE name = 'manifest-resource-writer'
`); err != nil {
			t.Fatal(err)
		}
		// The role-binding memo is per request, so the next request sees this.
		before := agent.mutations()
		response := submit(
			"manifest-resource-writer",
			"delete",
			manifestTestDocument("v1", "ConfigMap", "team-a", "app-config"),
		)
		if response.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403: %s", response.Code, response.Body)
		}
		if agent.mutations() != before {
			t.Fatal("a refused delete reached the Cluster")
		}
	})

	// Events resolve to a real resource and are refused whatever the caller holds,
	// so the strongest role is the one to try it with.
	t.Run("Events are refused under the full role", func(t *testing.T) {
		before := agent.mutations()
		response := submit(
			"manifest-full",
			"apply",
			manifestTestDocument("v1", "Event", "team-a", "something-happened"),
		)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400: %s", response.Code, response.Body)
		}
		if agent.mutations() != before {
			t.Fatal("an Event document reached the Cluster")
		}
	})

	// Without a CSRF token the request never reaches authorization at all.
	t.Run("CSRF is required", func(t *testing.T) {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(
			http.MethodPost,
			"/api/v1/clusters/"+clusterID+"/kubernetes/manifests/apply?confirm=true",
			strings.NewReader(manifestTestDocument("v1", "ConfigMap", "team-a", "c")),
		)
		request.Header.Set("Content-Type", "application/yaml")
		request.AddCookie(sessions["manifest-full"])
		router.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403: %s", response.Code, response.Body)
		}
	})

	// A dry run reports the verdicts instead of refusing, which is how the Console
	// can show which permission each document needs before offering to execute.
	t.Run("a dry run reports refusals per document", func(t *testing.T) {
		response := httptest.NewRecorder()
		mixed := manifestTestDocument("v1", "ConfigMap", "team-a", "app-config") +
			"---\n" + manifestTestDocument("v1", "Secret", "team-a", "app-secret")
		request := httptest.NewRequest(
			http.MethodPost,
			"/api/v1/clusters/"+clusterID+"/kubernetes/manifests/apply?dry_run=true",
			strings.NewReader(mixed),
		)
		request.Header.Set("Content-Type", "application/yaml")
		request.Header.Set(idempotencyKeyHeaderName, "manifest-e2e-dry-run-key")
		request.Header.Set("X-CSRF-Token", csrf["manifest-namespace-manager"])
		request.AddCookie(sessions["manifest-namespace-manager"])
		router.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", response.Code, response.Body)
		}
		var body struct {
			Data struct {
				Allowed   bool `json:"allowed"`
				Documents []struct {
					Kind       string `json:"kind"`
					Status     string `json:"status"`
					Permission string `json:"permission"`
				} `json:"documents"`
			} `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Data.Allowed || len(body.Data.Documents) != 2 {
			t.Fatalf("unexpected dry-run body: %s", response.Body)
		}
		wantPermission := map[string]string{
			"ConfigMap": "cluster.resource.create",
			"Secret":    "cluster.secret.manage",
		}
		for _, document := range body.Data.Documents {
			if document.Status != "refused" {
				t.Errorf("%s status = %q, want refused", document.Kind, document.Status)
			}
			if document.Permission != wantPermission[document.Kind] {
				t.Errorf(
					"%s names permission %q, want %q",
					document.Kind, document.Permission, wantPermission[document.Kind],
				)
			}
		}
	})
}

func manifestTestDocument(
	apiVersion string,
	kind string,
	namespace string,
	name string,
) string {
	builder := &strings.Builder{}
	builder.WriteString("apiVersion: " + apiVersion + "\n")
	builder.WriteString("kind: " + kind + "\n")
	builder.WriteString("metadata:\n")
	builder.WriteString("  name: " + name + "\n")
	if namespace != "" {
		builder.WriteString("  namespace: " + namespace + "\n")
	}
	return builder.String()
}

func manifestTestLogin(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	router *gin.Engine,
	role string,
	tenantID string,
	projectID string,
) (*http.Cookie, string) {
	t.Helper()
	password := []byte("a sufficiently long manifest end to end passphrase")
	passwordHash, err := auth.HashPassword(password, auth.DefaultPasswordParams())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO users (
    id, username_normalized, display_name, password_hash, status, password_changed_at
)
VALUES (gen_random_uuid(), $1, $1, $2, 'active', now())
`, role, passwordHash); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO role_bindings (id, subject_id, role, scope_type, tenant_id, project_id)
SELECT gen_random_uuid(), id, $1, 'project', $2, $3 FROM users
WHERE username_normalized = $1
`, role, tenantID, projectID); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/auth/login",
		strings.NewReader(
			`{"username":"`+role+`","password":"`+string(password)+`"}`,
		),
	)
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = "192.0.2.90:1234"
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("login %s: status = %d: %s", role, response.Code, response.Body)
	}
	cookies := response.Result().Cookies()
	return findCookie(t, cookies, sessionCookieName),
		findCookie(t, cookies, csrfCookieName).Value
}

// manifestTestAgent stands in for a Cluster: it answers discovery with a fixed
// catalog, reports every object as absent so each document is a create, and
// accepts every mutation while counting them.
//
// Counting is the assertion that matters. "Was this refused" is a status code,
// but "did anything reach the Cluster" is the property the whole-request refusal
// exists to guarantee, and only the Agent side can answer it.
type manifestTestAgent struct {
	mutex   sync.Mutex
	written int
}

func newManifestTestAgent() *manifestTestAgent {
	return &manifestTestAgent{}
}

func (agent *manifestTestAgent) mutations() int {
	agent.mutex.Lock()
	defer agent.mutex.Unlock()
	return agent.written
}

func (agent *manifestTestAgent) RequestResource(
	_ context.Context,
	_ string,
	request *agentv1.ResourceRequest,
	_ io.Reader,
	responseBody io.Writer,
) (*agentv1.ResourceResponse, error) {
	if request.GetVerb() == agentv1.ResourceVerb_RESOURCE_VERB_DISCOVER {
		return manifestTestWriteJSON(responseBody, manifestTestCatalog())
	}
	// Every object is absent, so every document plans as a create and the create
	// half of the permission split is what gets exercised.
	return &agentv1.ResourceResponse{
		Result:               agentv1.ResultCode_RESULT_CODE_NOT_FOUND,
		KubernetesStatusCode: http.StatusNotFound,
		Reason:               "NotFound",
	}, nil
}

func (agent *manifestTestAgent) RequestResourceMutation(
	_ context.Context,
	_ string,
	request *agentv1.ResourceRequest,
	requestBody io.Reader,
	responseBody io.Writer,
	_ string,
) (*agentv1.ResourceResponse, error) {
	agent.mutex.Lock()
	agent.written++
	agent.mutex.Unlock()

	if request.GetVerb() == agentv1.ResourceVerb_RESOURCE_VERB_DELETE {
		return &agentv1.ResourceResponse{
			Result:               agentv1.ResultCode_RESULT_CODE_OK,
			KubernetesStatusCode: http.StatusOK,
		}, nil
	}
	// Echoed back with an identity, which is what the resource layer checks the
	// response against before it will believe the write happened.
	submitted, err := io.ReadAll(requestBody)
	if err != nil {
		return nil, err
	}
	var object map[string]any
	if err := json.Unmarshal(submitted, &object); err != nil {
		return nil, err
	}
	metadata, _ := object["metadata"].(map[string]any)
	if metadata == nil {
		metadata = map[string]any{}
		object["metadata"] = metadata
	}
	metadata["uid"] = "00000000-0000-4000-8000-000000000001"
	metadata["resourceVersion"] = "1"
	return manifestTestWriteJSON(responseBody, object)
}

func manifestTestWriteJSON(
	writer io.Writer,
	value any,
) (*agentv1.ResourceResponse, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if _, err := writer.Write(body); err != nil {
		return nil, err
	}
	return &agentv1.ResourceResponse{
		Result:               agentv1.ResultCode_RESULT_CODE_OK,
		KubernetesStatusCode: http.StatusOK,
		ContentType:          "application/json",
		BodySize:             uint64(len(body)),
	}, nil
}

// The catalog a real Agent reports: Secrets are absent from it on purpose, which
// is why the manifest catalog declares that entry rather than discovering it.
// Leaving them out here is what keeps this test honest about that.
func manifestTestCatalog() kubernetescatalog.Catalog {
	verbs := []string{"get", "list", "create", "update", "patch", "delete"}
	return kubernetescatalog.Catalog{
		CustomResourcesKnown: true,
		Resources: []kubernetescatalog.Resource{
			{Version: "v1", Resource: "configmaps", Kind: "ConfigMap", Namespaced: true, Verbs: verbs},
			{Version: "v1", Resource: "namespaces", Kind: "Namespace", Namespaced: false, Verbs: verbs},
			{Version: "v1", Resource: "serviceaccounts", Kind: "ServiceAccount", Namespaced: true, Verbs: verbs},
			{Version: "v1", Resource: "events", Kind: "Event", Namespaced: true, Verbs: verbs},
			{
				Group: "apps", Version: "v1", Resource: "deployments", Kind: "Deployment",
				Namespaced: true, Verbs: verbs,
			},
			{
				Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "roles",
				Kind: "Role", Namespaced: true, Verbs: verbs,
			},
		},
	}
}

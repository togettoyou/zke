package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/rbac"
)

func TestProtectedNamespacePermission(t *testing.T) {
	t.Parallel()
	authorization := &Authorization{}

	tests := []struct {
		name       string
		method     string
		route      string
		path       string
		query      string
		body       string
		permission rbac.Permission
	}{
		{name: "agent resource", method: http.MethodPut, route: "/api/v1/clusters/:cluster_id/namespaces/:namespace_name/configmaps/:name", path: "/api/v1/clusters/cluster/namespaces/zke-system/configmaps/config", permission: rbac.PermissionClusterAgentNamespaceManage},
		{name: "Kubernetes system resource", method: http.MethodDelete, route: "/api/v1/clusters/:cluster_id/namespaces/:namespace_name/pods/:name", path: "/api/v1/clusters/cluster/namespaces/kube-system/pods/pod", permission: rbac.PermissionClusterSystemNamespaceManage},
		{name: "default workload stays ordinary", method: http.MethodDelete, route: "/api/v1/clusters/:cluster_id/namespaces/:namespace_name/pods/:name", path: "/api/v1/clusters/cluster/namespaces/default/pods/pod"},
		{name: "default Namespace lifecycle", method: http.MethodDelete, route: "/api/v1/clusters/:cluster_id/namespaces/:namespace_name", path: "/api/v1/clusters/cluster/namespaces/default", permission: rbac.PermissionClusterSystemNamespaceManage},
		{name: "generic query", method: http.MethodPatch, route: "/api/v1/clusters/:cluster_id/kubernetes/resources/:resource_name", path: "/api/v1/clusters/cluster/kubernetes/resources/object", query: "namespace=kube-public", permission: rbac.PermissionClusterSystemNamespaceManage},
		{name: "generic Namespace object", method: http.MethodPut, route: "/api/v1/clusters/:cluster_id/kubernetes/resources/:resource_name", path: "/api/v1/clusters/cluster/kubernetes/resources/zke-system", query: "version=v1&resource=namespaces", permission: rbac.PermissionClusterAgentNamespaceManage},
		{name: "generic Namespace create", method: http.MethodPost, route: "/api/v1/clusters/:cluster_id/kubernetes/resources", path: "/api/v1/clusters/cluster/kubernetes/resources", query: "version=v1&resource=namespaces", body: `{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"kube-node-lease"}}`, permission: rbac.PermissionClusterSystemNamespaceManage},
		{name: "Namespace create body", method: http.MethodPost, route: "/api/v1/clusters/:cluster_id/namespaces", path: "/api/v1/clusters/cluster/namespaces", body: `{"name":"kube-addon","dry_run":true}`, permission: rbac.PermissionClusterSystemNamespaceManage},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			router := gin.New()
			var got rbac.Permission
			router.Handle(testCase.method, testCase.route, func(c *gin.Context) {
				got = authorization.protectedNamespacePermission(c, "zke-system")
			})
			path := testCase.path
			if testCase.query != "" {
				path += "?" + testCase.query
			}
			router.ServeHTTP(recorder, httptest.NewRequest(testCase.method, path, strings.NewReader(testCase.body)))
			if got != testCase.permission {
				t.Fatalf("permission = %q, want %q", got, testCase.permission)
			}
		})
	}
}

func TestEffectiveClusterPermissionReplacesGenericMutationOnly(t *testing.T) {
	t.Parallel()
	authorization := &Authorization{}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPut, "/", nil)
	c.Params = gin.Params{{Key: "namespace_name", Value: "zke-system"}}

	if got := authorization.effectiveClusterPermission(c, rbac.PermissionClusterResourceUpdate, "zke-system"); got != rbac.PermissionClusterAgentNamespaceManage {
		t.Fatalf("resource permission = %q", got)
	}
	if got := authorization.effectiveClusterPermission(c, rbac.PermissionClusterRBACManage, "zke-system"); got != rbac.PermissionClusterRBACManage {
		t.Fatalf("RBAC permission = %q", got)
	}
}

func TestEffectiveClusterPermissionUsesNamespaceLifecycleGrantForOrdinaryNamespace(t *testing.T) {
	t.Parallel()
	authorization := &Authorization{}
	recorder := httptest.NewRecorder()
	router := gin.New()
	var got rbac.Permission
	router.PUT(
		"/api/v1/clusters/:cluster_id/kubernetes/resources/:resource_name",
		func(c *gin.Context) {
			got = authorization.effectiveClusterPermission(c, rbac.PermissionClusterResourceUpdate, "zke-system")
		},
	)
	router.ServeHTTP(recorder, httptest.NewRequest(
		http.MethodPut,
		"/api/v1/clusters/cluster/kubernetes/resources/team-a?version=v1&resource=namespaces",
		nil,
	))
	if got != rbac.PermissionClusterNamespaceManage {
		t.Fatalf("permission = %q, want %q", got, rbac.PermissionClusterNamespaceManage)
	}
}

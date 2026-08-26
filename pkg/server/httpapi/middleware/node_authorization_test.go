package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/rbac"
)

// Every generic write that can reach a Node object answers to
// `cluster.node.manage`, and only those do. The Node's YAML, its labels and
// `spec.unschedulable` all travel through these routes as an ordinary resource
// write, so a check that looked only at the route would let
// `cluster.resource.update` relabel every Node in the Cluster.
func TestEffectiveClusterPermissionUsesNodeGrantForNodeObjects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		method     string
		route      string
		path       string
		requested  rbac.Permission
		permission rbac.Permission
	}{
		{
			name:       "update",
			method:     http.MethodPut,
			route:      "/api/v1/clusters/:cluster_id/kubernetes/resources/:resource_name",
			path:       "/api/v1/clusters/cluster/kubernetes/resources/worker-1?version=v1&resource=nodes",
			requested:  rbac.PermissionClusterResourceUpdate,
			permission: rbac.PermissionClusterNodeManage,
		},
		{
			name:       "patch",
			method:     http.MethodPatch,
			route:      "/api/v1/clusters/:cluster_id/kubernetes/resources/:resource_name",
			path:       "/api/v1/clusters/cluster/kubernetes/resources/worker-1?version=v1&resource=nodes",
			requested:  rbac.PermissionClusterResourceUpdate,
			permission: rbac.PermissionClusterNodeManage,
		},
		{
			name:       "yaml",
			method:     http.MethodPut,
			route:      "/api/v1/clusters/:cluster_id/kubernetes/resources/:resource_name/yaml",
			path:       "/api/v1/clusters/cluster/kubernetes/resources/worker-1/yaml?version=v1&resource=nodes",
			requested:  rbac.PermissionClusterResourceUpdate,
			permission: rbac.PermissionClusterNodeManage,
		},
		{
			name:       "delete",
			method:     http.MethodDelete,
			route:      "/api/v1/clusters/:cluster_id/kubernetes/resources/:resource_name",
			path:       "/api/v1/clusters/cluster/kubernetes/resources/worker-1?version=v1&resource=nodes",
			requested:  rbac.PermissionClusterResourceDelete,
			permission: rbac.PermissionClusterNodeManage,
		},
		{
			name:       "create",
			method:     http.MethodPost,
			route:      "/api/v1/clusters/:cluster_id/kubernetes/resources",
			path:       "/api/v1/clusters/cluster/kubernetes/resources?version=v1&resource=nodes",
			requested:  rbac.PermissionClusterResourceCreate,
			permission: rbac.PermissionClusterNodeManage,
		},
		// Reads are unchanged: a Node's labels are ordinary `cluster.read`, and
		// narrowing them here would hide the object the editor opens on.
		{
			name:       "read",
			method:     http.MethodGet,
			route:      "/api/v1/clusters/:cluster_id/kubernetes/resources/:resource_name",
			path:       "/api/v1/clusters/cluster/kubernetes/resources/worker-1?version=v1&resource=nodes",
			requested:  rbac.PermissionClusterRead,
			permission: rbac.PermissionClusterRead,
		},
		// A resource whose name merely ends in "nodes" is a different resource.
		{
			name:       "other resource",
			method:     http.MethodPut,
			route:      "/api/v1/clusters/:cluster_id/kubernetes/resources/:resource_name",
			path:       "/api/v1/clusters/cluster/kubernetes/resources/worker-1?group=metrics.k8s.io&version=v1beta1&resource=nodes",
			requested:  rbac.PermissionClusterResourceUpdate,
			permission: rbac.PermissionClusterResourceUpdate,
		},
		// The Node permission does not displace the sensitive families, which
		// keep their own boundary the way they do for Namespaces.
		{
			name:       "sensitive family",
			method:     http.MethodPut,
			route:      "/api/v1/clusters/:cluster_id/kubernetes/resources/:resource_name",
			path:       "/api/v1/clusters/cluster/kubernetes/resources/worker-1?version=v1&resource=nodes",
			requested:  rbac.PermissionClusterRBACManage,
			permission: rbac.PermissionClusterRBACManage,
		},
	}

	authorization := &Authorization{}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			recorder := httptest.NewRecorder()
			router := gin.New()
			var got rbac.Permission
			router.Handle(testCase.method, testCase.route, func(c *gin.Context) {
				got = authorization.effectiveClusterPermission(c, testCase.requested, "zke-system")
			})
			router.ServeHTTP(recorder, httptest.NewRequest(testCase.method, testCase.path, nil))
			if got != testCase.permission {
				t.Fatalf("permission = %q, want %q", got, testCase.permission)
			}
		})
	}
}

package middleware

import (
	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/rbac"
)

type ProtectedNamespaceGrant struct {
	System bool
	Agent  bool
}

const protectedNamespaceGrantContextKey = "zke.protected_namespace_grant"

// ResolveClusterProtectedNamespaceGrant records the two exceptional mutation
// grants for operations such as Node drain whose affected Namespaces are known
// only after the Server reads the target Cluster.
func (authorization *Authorization) ResolveClusterProtectedNamespaceGrant(
	clusterParameter string,
) gin.HandlerFunc {
	return func(c *gin.Context) {
		identity, exists := Identity(c)
		if !exists {
			c.Next()
			return
		}
		clusterID := c.Param(clusterParameter)
		c.Set(protectedNamespaceGrantContextKey, ProtectedNamespaceGrant{
			System: authorization.holdsCluster(c, identity.User.ID, rbac.PermissionClusterSystemNamespaceManage, clusterID),
			Agent:  authorization.holdsCluster(c, identity.User.ID, rbac.PermissionClusterAgentNamespaceManage, clusterID),
		})
		c.Next()
	}
}

func ClusterProtectedNamespaceGrant(c *gin.Context) ProtectedNamespaceGrant {
	value, exists := c.Get(protectedNamespaceGrantContextKey)
	if !exists {
		return ProtectedNamespaceGrant{}
	}
	grant, _ := value.(ProtectedNamespaceGrant)
	return grant
}

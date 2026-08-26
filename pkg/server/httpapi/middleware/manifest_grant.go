package middleware

import (
	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/rbac"
)

// ManifestGrant reports every Cluster permission a manifest request might need,
// resolved once before any document is read.
//
// It exists because a manifest is the one request in the platform whose required
// permissions are not knowable from its route. Every other write names one
// object of one family in its URL, so `RequireCluster` can decide it before the
// handler runs; a manifest carries a Deployment, a Secret and a RoleBinding in
// one body, and those answer to three permissions the typed APIs deliberately
// keep apart. Deciding per document is therefore not a weakening of the route
// check — it is the only place the question can be asked at all.
//
// Like SecretGrant, resolving is not deciding: this never refuses a request. The
// handler compares the grant against what the documents actually ask for, and
// refuses the request whole when any document is not covered. Plain booleans
// rather than the resource layer's own type, so this package keeps depending on
// nothing but authorization.
type ManifestGrant struct {
	ResourceCreate        bool
	ResourceUpdate        bool
	ResourceDelete        bool
	NamespaceManage       bool
	NodeManage            bool
	SecretRead            bool
	SecretManage          bool
	RBACManage            bool
	SystemNamespaceManage bool
	AgentNamespaceManage  bool
}

const manifestGrantContextKey = "zke.manifest_grant"

// ResolveClusterManifestGrant records what the caller may write on the Cluster a
// manifest request targets.
//
// Installed after the route's own authorization check, so it runs only for
// requests that were going to be served, and inside the request-scoped binding
// cache, so seven questions cost one query rather than seven.
//
// A lookup that fails for any reason other than denial grants nothing, exactly
// as ResolveClusterSecretGrant does: narrowing what a request may write is the
// safe direction, and the caller learns which document was refused rather than
// having an unrelated lookup failure reported as the operation's own.
func (authorization *Authorization) ResolveClusterManifestGrant(
	clusterParameter string,
) gin.HandlerFunc {
	return func(c *gin.Context) {
		identity, exists := Identity(c)
		if !exists {
			c.Next()
			return
		}
		clusterID := c.Param(clusterParameter)
		holds := func(permission rbac.Permission) bool {
			return authorization.holdsCluster(
				c, identity.User.ID, permission, clusterID,
			)
		}
		c.Set(manifestGrantContextKey, ManifestGrant{
			ResourceCreate:        holds(rbac.PermissionClusterResourceCreate),
			ResourceUpdate:        holds(rbac.PermissionClusterResourceUpdate),
			ResourceDelete:        holds(rbac.PermissionClusterResourceDelete),
			NamespaceManage:       holds(rbac.PermissionClusterNamespaceManage),
			NodeManage:            holds(rbac.PermissionClusterNodeManage),
			SecretRead:            holds(rbac.PermissionClusterSecretRead),
			SecretManage:          holds(rbac.PermissionClusterSecretManage),
			RBACManage:            holds(rbac.PermissionClusterRBACManage),
			SystemNamespaceManage: holds(rbac.PermissionClusterSystemNamespaceManage),
			AgentNamespaceManage:  holds(rbac.PermissionClusterAgentNamespaceManage),
		})
		c.Next()
	}
}

// ClusterManifestGrant reports what ResolveClusterManifestGrant recorded. A
// request that never ran it grants nothing, which refuses every document rather
// than admitting one.
func ClusterManifestGrant(c *gin.Context) ManifestGrant {
	value, exists := c.Get(manifestGrantContextKey)
	if !exists {
		return ManifestGrant{}
	}
	grant, ok := value.(ManifestGrant)
	if !ok {
		return ManifestGrant{}
	}
	return grant
}

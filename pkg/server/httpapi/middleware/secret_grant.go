package middleware

import (
	"context"
	"errors"
	"log/slog"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/rbac"
)

/*
 * SecretGrant reports the Secret permissions the caller holds on the Cluster a
 * request targets.
 *
 * It exists because a Kubernetes PolicyRule is a way to hand access to somebody
 * else: a Role granting `get` on `secrets` gives every subject bound to it what
 * `cluster.secret.read` gives its holder. Writing such a rule therefore has to
 * require the permission it hands out, or `cluster.rbac.manage` alone would be a
 * way around every Secret permission in the platform.
 *
 * Two booleans rather than the resource layer's own type, so this package keeps
 * depending on nothing but authorization.
 */
type SecretGrant struct {
	Read   bool
	Manage bool
}

/*
 * ResolveClusterSecretGrant records what the caller may hand out, without
 * refusing anything.
 *
 * Unlike RequireCluster this never aborts: not holding the permission is a
 * normal outcome that narrows what the request may write, not a reason to reject
 * it. A caller with no Secret permissions can still manage Roles — just not Roles
 * that grant Secret access.
 *
 * Installed after the route's own authorization check, so it runs only for
 * requests that were going to be served, and inside the request-scoped binding
 * cache, so the extra questions cost no extra query.
 *
 * A lookup that fails for any reason other than denial grants nothing. Reporting
 * an error here would turn a Secret-permission lookup into a failure of an
 * operation that may have nothing to do with Secrets; refusing to widen what the
 * request may write is the safe direction, and the write itself still succeeds or
 * fails on its own merits.
 */
func (authorization *Authorization) ResolveClusterSecretGrant(
	clusterParameter string,
) gin.HandlerFunc {
	return func(c *gin.Context) {
		identity, exists := Identity(c)
		if !exists {
			c.Next()
			return
		}
		clusterID := c.Param(clusterParameter)
		grant := SecretGrant{
			Read: authorization.holdsCluster(
				c, identity.User.ID, rbac.PermissionClusterSecretRead, clusterID,
			),
			Manage: authorization.holdsCluster(
				c, identity.User.ID, rbac.PermissionClusterSecretManage, clusterID,
			),
		}
		c.Set(secretGrantContextKey, grant)
		c.Next()
	}
}

const secretGrantContextKey = "zke.secret_grant"

// ClusterSecretGrant reports what ResolveClusterSecretGrant recorded. A request
// that never ran it grants nothing, which is what a handler reached by some
// other route should assume.
func ClusterSecretGrant(c *gin.Context) SecretGrant {
	value, exists := c.Get(secretGrantContextKey)
	if !exists {
		return SecretGrant{}
	}
	grant, ok := value.(SecretGrant)
	if !ok {
		return SecretGrant{}
	}
	return grant
}

func (authorization *Authorization) holdsCluster(
	c *gin.Context,
	userID string,
	permission rbac.Permission,
	clusterID string,
) bool {
	operationContext, cancel := context.WithTimeout(
		c.Request.Context(),
		authorization.config.OperationTimeout,
	)
	defer cancel()

	_, err := authorization.service.AuthorizeCluster(
		operationContext, userID, permission, clusterID,
	)
	if err == nil {
		return true
	}
	if errors.Is(err, rbac.ErrDenied) || errors.Is(err, rbac.ErrInvalidScope) {
		return false
	}
	// Not a denial: the question could not be answered. Logged because a
	// persistent failure here silently narrows what operators can write, which
	// would otherwise surface only as a confusing refusal.
	authorization.logger.Warn(
		"resolve Cluster Secret grant",
		slog.String("request_id", RequestID(c)),
		slog.String("permission", string(permission)),
		slog.String("cluster_id", clusterID),
		slog.String("error", err.Error()),
	)
	return false
}

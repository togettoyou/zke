package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/audit"
	"github.com/togettoyou/zke/pkg/server/auditaction"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
)

// Helm releases, read-only.
//
// A Helm release is not a Kubernetes kind. It is a Secret of type
// `helm.sh/release.v1` holding the chart, the values it was given and the
// manifest it rendered, and the objects it created are ordinary Kubernetes
// objects with no back-reference to it. So the question "what applications are
// installed in this Namespace, at which chart version, and with what values" has
// no answer anywhere else in the container service — the resource browser shows
// the Deployments, never the release that produced them.
//
// These endpoints answer it and nothing more. ZKE does not install, upgrade,
// roll back or uninstall a release: doing that correctly means running Helm's
// own engine — hooks, ordering, the whole chart rendering pipeline — and a
// half-implementation that wrote release Secrets itself would corrupt the
// history the real `helm` client depends on.
//
// The permission follows the storage, not the appearance. Reading a release
// hands back the values the chart was installed with, which routinely include a
// password, so every route here requires `cluster.secret.read` on top of
// `cluster.read` and each one is audited the way a Secret read is — listing
// separately from reading, because a listing returns no values at all.

type kubernetesHelmReleaseService interface {
	ListHelmReleases(
		context.Context,
		kubernetesresource.ListHelmReleasesInput,
	) (kubernetesresource.HelmReleasePage, error)
	ListHelmReleaseRevisions(
		context.Context,
		string,
		string,
		string,
	) (kubernetesresource.HelmReleasePage, error)
	GetHelmRelease(
		context.Context,
		string,
		string,
		string,
		int64,
	) (kubernetesresource.HelmReleaseDetail, error)
}

type kubernetesHelmReleaseHandler struct {
	baseHandler
	service kubernetesHelmReleaseService
}

func newKubernetesHelmReleaseHandler(
	logger *slog.Logger,
	service kubernetesHelmReleaseService,
	auditService *audit.Service,
	operationTimeout time.Duration,
) *kubernetesHelmReleaseHandler {
	return &kubernetesHelmReleaseHandler{
		baseHandler: newBaseHandler(logger, auditService, operationTimeout),
		service:     service,
	}
}

func (handler *kubernetesHelmReleaseHandler) list(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	actor, _ := httpmiddleware.Identity(c)
	target := helmReleaseTarget(c, "")
	if len(c.Request.URL.Query()) != 0 {
		handler.recordRead(c, actor.User.ID, auditaction.KubernetesHelmReleaseList, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "Helm release list does not accept query parameters")
		return
	}
	if handler.service == nil {
		handler.recordRead(c, actor.User.ID, auditaction.KubernetesHelmReleaseList, target, "failed")
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Helm release query is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.ListHelmReleases(ctx, kubernetesresource.ListHelmReleasesInput{
		ClusterID: c.Param("cluster_id"),
		Namespace: c.Param("namespace_name"),
	})
	cancel()
	handler.recordRead(c, actor.User.ID, auditaction.KubernetesHelmReleaseList, target, readResult(err))
	if handler.respondHelmReleaseError(c, "list Helm releases", err) {
		return
	}
	writeSuccess(c, http.StatusOK, result)
}

// revisions is the release's stored history. It is a listing rather than a read:
// it names revisions and their outcome, and returns no values.
func (handler *kubernetesHelmReleaseHandler) revisions(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	actor, _ := httpmiddleware.Identity(c)
	target := helmReleaseTarget(c, c.Param("release_name"))
	if len(c.Request.URL.Query()) != 0 {
		handler.recordRead(c, actor.User.ID, auditaction.KubernetesHelmReleaseList, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "Helm release history does not accept query parameters")
		return
	}
	if handler.service == nil {
		handler.recordRead(c, actor.User.ID, auditaction.KubernetesHelmReleaseList, target, "failed")
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Helm release query is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.ListHelmReleaseRevisions(
		ctx,
		c.Param("cluster_id"),
		c.Param("namespace_name"),
		c.Param("release_name"),
	)
	cancel()
	handler.recordRead(c, actor.User.ID, auditaction.KubernetesHelmReleaseList, target, readResult(err))
	if handler.respondHelmReleaseError(c, "list Helm release revisions", err) {
		return
	}
	writeSuccess(c, http.StatusOK, result)
}

// get reads one revision, values included. `revision` selects an older one; its
// absence means whichever revision storage currently holds as newest.
func (handler *kubernetesHelmReleaseHandler) get(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	actor, _ := httpmiddleware.Identity(c)
	target := helmReleaseTarget(c, c.Param("release_name"))
	revision, err := parseHelmReleaseRevision(c)
	if err != nil {
		handler.recordRead(c, actor.User.ID, auditaction.KubernetesHelmReleaseRead, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid Helm release revision")
		return
	}
	if handler.service == nil {
		handler.recordRead(c, actor.User.ID, auditaction.KubernetesHelmReleaseRead, target, "failed")
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Helm release query is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.GetHelmRelease(
		ctx,
		c.Param("cluster_id"),
		c.Param("namespace_name"),
		c.Param("release_name"),
		revision,
	)
	cancel()
	handler.recordRead(c, actor.User.ID, auditaction.KubernetesHelmReleaseRead, target, readResult(err))
	if handler.respondHelmReleaseError(c, "get Helm release", err) {
		return
	}
	writeSuccess(c, http.StatusOK, result)
}

func parseHelmReleaseRevision(c *gin.Context) (int64, error) {
	query := c.Request.URL.Query()
	if err := validateQueryNames(query, map[string]struct{}{"revision": {}}); err != nil {
		return 0, err
	}
	value := query.Get("revision")
	if value == "" {
		return 0, nil
	}
	revision, err := strconv.ParseInt(value, 10, 64)
	if err != nil || revision <= 0 {
		return 0, errors.New("invalid Helm release revision")
	}
	return revision, nil
}

// The audit target names the Secret family the release lives in rather than a
// family of its own, because that is what was read. An auditor asking "who read
// Secrets in this Namespace" has to find these too.
func helmReleaseTarget(c *gin.Context, name string) string {
	target := resourceTargetName(
		kubernetesresource.SecretResourceIdentity(),
		c.Param("namespace_name"),
		"",
	)
	if name != "" {
		target += " helm_release:" + name
	}
	return target
}

func (handler *kubernetesHelmReleaseHandler) recordRead(c *gin.Context, actorID, action, target, result string) {
	handler.recordOperation(c, auditedOperation{
		Scope:       auditScopeCluster,
		ActorUserID: actorID,
		Action:      action,
		TargetType:  auditaction.TargetKubernetesResource,
		TargetName:  target,
		Result:      result,
	})
}

func (handler *kubernetesHelmReleaseHandler) respondHelmReleaseError(
	c *gin.Context,
	operation string,
	err error,
) bool {
	if err == nil {
		return false
	}
	return handler.respondError(c, operation, err,
		append([]errorMapping{
			{
				kubernetesresource.ErrHelmReleaseNotFound,
				http.StatusNotFound,
				"helm_release_not_found",
				"Helm release not found in this Namespace",
			},
			// 502 rather than 500: the payload came from the Cluster, and this
			// Server is reporting what it received rather than failing itself.
			{
				kubernetesresource.ErrHelmReleaseUnreadable,
				http.StatusBadGateway,
				"helm_release_unreadable",
				"Helm release payload is not in a format ZKE can decode",
			},
			{
				kubernetesresource.ErrHelmReleaseInventoryTruncated,
				http.StatusUnprocessableEntity,
				"helm_release_inventory_truncated",
				"Namespace holds more Helm release revisions than one inventory reads",
			},
		}, kubernetesResourceErrorMappings()...)...,
	)
}

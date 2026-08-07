package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/audit"
	"github.com/togettoyou/zke/pkg/server/auditaction"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
	"github.com/togettoyou/zke/pkg/server/kubernetesyaml"
)

/*
 * YAML for one Secret.
 *
 * A route of its own, under `cluster.secret.read` and `cluster.secret.manage`,
 * for the same reason the typed Secret API has its own: reading configuration
 * and reading credentials are different asks. The generic YAML endpoint still
 * refuses `core/v1 Secret` outright, and the Agent still refuses one that does
 * not carry the Secret API's flag — this handler reaches a Secret because it
 * runs over the Secret service's own access, not because the refusal was
 * relaxed.
 *
 * What the document carries is every value in the Secret, Base64-encoded as
 * Kubernetes stores it. That is a larger exposure than the detail page, which
 * masks each value until it is asked for, and it is why the Console names it
 * before opening this.
 */
type kubernetesSecretYAMLHandler struct {
	kubernetesYAMLHandler
}

func newKubernetesSecretYAMLHandler(
	logger *slog.Logger,
	service kubernetesYAMLService,
	auditService *audit.Service,
	operationTimeout time.Duration,
) *kubernetesSecretYAMLHandler {
	return &kubernetesSecretYAMLHandler{
		kubernetesYAMLHandler: kubernetesYAMLHandler{
			baseHandler: newBaseHandler(logger, auditService, operationTimeout),
			service:     service,
		},
	}
}

func (handler *kubernetesSecretYAMLHandler) get(c *gin.Context) {
	setYAMLResponseHeaders(c)
	identity, _ := httpmiddleware.Identity(c)
	target := resourceTargetName(
		kubernetesresource.SecretResourceIdentity(),
		c.Param("namespace_name"),
		c.Param("secret_name"),
	)
	// Recorded under the same action as the typed detail read. The document
	// carries more than the detail page does — every value at once — but the
	// question the trail has to answer is the same one, and splitting it across
	// two action names would let a reviewer filtering on the obvious one miss
	// the larger exposure.
	if len(c.Request.URL.Query()) != 0 {
		handler.recordSecretRead(c, identity.User.ID, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "Secret YAML does not accept query parameters")
		return
	}
	if handler.service == nil {
		handler.recordSecretRead(c, identity.User.ID, target, "failed")
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Kubernetes YAML query is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.Get(ctx, kubernetesyaml.GetInput{
		ClusterID: c.Param("cluster_id"),
		Resource:  kubernetesresource.SecretResourceIdentity(),
		Namespace: c.Param("namespace_name"),
		Name:      c.Param("secret_name"),
	})
	cancel()
	handler.recordSecretRead(c, identity.User.ID, target, readResult(err))
	if handler.respondYAMLError(c, "get Kubernetes Secret YAML", err) {
		return
	}
	writeYAML(c, result, false)
}

func (handler *kubernetesSecretYAMLHandler) recordSecretRead(
	c *gin.Context,
	actorID string,
	target string,
	result string,
) {
	handler.recordOperation(c, auditedOperation{
		Scope:       auditScopeCluster,
		ActorUserID: actorID,
		Action:      auditaction.KubernetesSecretRead,
		TargetType:  auditaction.TargetKubernetesResource,
		TargetName:  target,
		Result:      result,
	})
}

func (handler *kubernetesSecretYAMLHandler) update(c *gin.Context) {
	setYAMLResponseHeaders(c)
	query, err := parseYAMLMutationQuery(c.Request.URL.Query())
	identity, _ := httpmiddleware.Identity(c)
	target := resourceTargetName(
		kubernetesresource.SecretResourceIdentity(),
		c.Param("namespace_name"),
		c.Param("secret_name"),
	)
	action := kubernetesMutationAuditAction(
		auditaction.KubernetesResourceUpdate,
		query.DryRun,
	)
	if err != nil {
		handler.recordKubernetesMutation(c, identity.User.ID, action, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid Secret YAML update request")
		return
	}
	if !query.DryRun && !query.Confirm {
		handler.recordKubernetesMutation(c, identity.User.ID, action, target, "failed")
		writeError(c, http.StatusBadRequest, "confirmation_required", "explicit confirmation is required")
		return
	}
	manifest, err := readYAMLManifest(c)
	if err != nil {
		handler.recordKubernetesMutation(c, identity.User.ID, action, target, "failed")
		writeYAMLRequestError(c, err)
		return
	}
	if handler.service == nil {
		handler.recordKubernetesMutation(c, identity.User.ID, action, target, "failed")
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Kubernetes YAML mutation is unavailable")
		return
	}

	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.Update(ctx, kubernetesyaml.UpdateInput{
		GetInput: kubernetesyaml.GetInput{
			ClusterID: c.Param("cluster_id"),
			Resource:  kubernetesresource.SecretResourceIdentity(),
			Namespace: c.Param("namespace_name"),
			Name:      c.Param("secret_name"),
		},
		Manifest:       manifest,
		DryRun:         query.DryRun,
		Confirm:        query.Confirm,
		FieldManager:   query.FieldManager,
		IdempotencyKey: c.GetHeader(idempotencyKeyHeaderName),
	})
	cancel()
	if err != nil {
		handler.recordKubernetesMutation(c, identity.User.ID, action, target, "failed")
	}
	if handler.respondYAMLError(c, "update Kubernetes Secret YAML", err) {
		return
	}
	handler.recordKubernetesMutation(c, identity.User.ID, action, target, "succeeded")
	writeYAML(c, result, query.DryRun)
}

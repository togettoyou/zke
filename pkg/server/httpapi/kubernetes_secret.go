package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/audit"
	"github.com/togettoyou/zke/pkg/server/auditaction"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
)

const maxKubernetesSecretMutationRequestBytes = 2 * 1024 * 1024

// Secrets, on endpoints of their own.
//
// Shaped like the ConfigMap endpoints because an operator manages the two the
// same way, and separated from them because the platform does not: these
// require `cluster.secret.read` and `cluster.secret.manage` rather than the
// general cluster permissions, and every one of them — the reads included —
// writes an audit record naming the Cluster, the Namespace and the object,
// without recording any part of what it held.
//
// Reads are audited here and not on the ConfigMap endpoints because a Secret
// read is the exposure itself. A permission can be withdrawn; a credential that
// has already been returned cannot be called back, so the only thing left to
// answer afterwards is who took it and when.

type kubernetesSecretService interface {
	ListSecrets(context.Context, kubernetesresource.ListSecretsInput) (kubernetesresource.SecretPage, error)
	GetSecret(context.Context, string, string, string) (kubernetesresource.SecretDetail, error)
	CreateSecret(context.Context, kubernetesresource.CreateSecretInput) (kubernetesresource.SecretDetail, error)
	UpdateSecret(context.Context, kubernetesresource.UpdateSecretInput) (kubernetesresource.SecretDetail, error)
	DeleteSecret(context.Context, kubernetesresource.DeleteSecretInput) error
}

type kubernetesSecretHandler struct {
	baseHandler
	service kubernetesSecretService
}

type createSecretRequest struct {
	Name        string            `json:"name"`
	Type        string            `json:"type"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	// Standard padded Base64, as the detail returns them.
	Data      map[string]string `json:"data"`
	Immutable bool              `json:"immutable"`
	DryRun    bool              `json:"dry_run"`
	Confirm   bool              `json:"confirm"`
}

type updateSecretRequest struct {
	UID             string            `json:"uid"`
	ResourceVersion string            `json:"resource_version"`
	Data            map[string]string `json:"data"`
	Immutable       *bool             `json:"immutable"`
	DryRun          bool              `json:"dry_run"`
	Confirm         bool              `json:"confirm"`
}

type deleteSecretRequest struct {
	UID             string `json:"uid"`
	ResourceVersion string `json:"resource_version"`
	DryRun          bool   `json:"dry_run"`
	Confirm         bool   `json:"confirm"`
}

func newKubernetesSecretHandler(
	logger *slog.Logger,
	service kubernetesSecretService,
	auditService *audit.Service,
	operationTimeout time.Duration,
) *kubernetesSecretHandler {
	return &kubernetesSecretHandler{
		baseHandler: newBaseHandler(logger, auditService, operationTimeout),
		service:     service,
	}
}

func (handler *kubernetesSecretHandler) list(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	actor, _ := httpmiddleware.Identity(c)
	target := handler.target(c, "")
	input, err := parseSecretListQuery(c.Request.URL.Query())
	if err != nil {
		handler.recordRead(c, actor.User.ID, auditaction.KubernetesSecretList, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid Secret query")
		return
	}
	if handler.service == nil {
		handler.recordRead(c, actor.User.ID, auditaction.KubernetesSecretList, target, "failed")
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Secret query is unavailable")
		return
	}
	input.ClusterID = c.Param("cluster_id")
	input.Namespace = c.Param("namespace_name")
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.ListSecrets(ctx, input)
	cancel()
	handler.recordRead(c, actor.User.ID, auditaction.KubernetesSecretList, target, readResult(err))
	if handler.respondSecretError(c, "list Kubernetes Secrets", err) {
		return
	}
	writeSuccess(c, http.StatusOK, result)
}

func (handler *kubernetesSecretHandler) get(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	actor, _ := httpmiddleware.Identity(c)
	target := handler.target(c, c.Param("secret_name"))
	if len(c.Request.URL.Query()) != 0 {
		handler.recordRead(c, actor.User.ID, auditaction.KubernetesSecretRead, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "Secret detail does not accept query parameters")
		return
	}
	if handler.service == nil {
		handler.recordRead(c, actor.User.ID, auditaction.KubernetesSecretRead, target, "failed")
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Secret query is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.GetSecret(
		ctx, c.Param("cluster_id"), c.Param("namespace_name"), c.Param("secret_name"),
	)
	cancel()
	handler.recordRead(c, actor.User.ID, auditaction.KubernetesSecretRead, target, readResult(err))
	if handler.respondSecretError(c, "get Kubernetes Secret", err) {
		return
	}
	writeSuccess(c, http.StatusOK, result)
}

func (handler *kubernetesSecretHandler) create(c *gin.Context) {
	target, actorID, ok := handler.mutationTarget(c, auditaction.KubernetesResourceCreate, "")
	if !ok {
		return
	}
	var request createSecretRequest
	if decodeJSONRequest(c, &request, maxKubernetesSecretMutationRequestBytes) != nil {
		handler.recordMutation(c, actorID, auditaction.KubernetesResourceCreate, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid Secret create request")
		return
	}
	target = handler.target(c, request.Name)
	action := kubernetesMutationAuditAction(auditaction.KubernetesResourceCreate, request.DryRun)
	if !request.DryRun && !request.Confirm {
		handler.recordMutation(c, actorID, action, target, "failed")
		writeError(c, http.StatusBadRequest, "confirmation_required", "explicit confirmation is required")
		return
	}
	if handler.service == nil {
		handler.recordMutation(c, actorID, action, target, "failed")
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Secret mutation is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.CreateSecret(ctx, kubernetesresource.CreateSecretInput{
		ClusterID: c.Param("cluster_id"), Namespace: c.Param("namespace_name"), Name: request.Name,
		Type: request.Type, Labels: request.Labels, Annotations: request.Annotations,
		Data: request.Data, Immutable: request.Immutable,
		DryRun: request.DryRun, Confirm: request.Confirm,
		IdempotencyKey: c.GetHeader(idempotencyKeyHeaderName),
	})
	cancel()
	if err != nil {
		handler.recordMutation(c, actorID, action, target, "failed")
	}
	if handler.respondSecretError(c, "create Kubernetes Secret", err) {
		return
	}
	handler.recordMutation(c, actorID, action, target, "succeeded")
	status := http.StatusCreated
	if request.DryRun {
		status = http.StatusOK
	}
	writeSuccess(c, status, gin.H{"resource": result, "dry_run": request.DryRun})
}

func (handler *kubernetesSecretHandler) update(c *gin.Context) {
	target, actorID, ok := handler.mutationTarget(c, auditaction.KubernetesResourceUpdate, c.Param("secret_name"))
	if !ok {
		return
	}
	var request updateSecretRequest
	if decodeJSONRequest(c, &request, maxKubernetesSecretMutationRequestBytes) != nil {
		handler.recordMutation(c, actorID, auditaction.KubernetesResourceUpdate, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid Secret update request")
		return
	}
	action := kubernetesMutationAuditAction(auditaction.KubernetesResourceUpdate, request.DryRun)
	if !request.DryRun && !request.Confirm {
		handler.recordMutation(c, actorID, action, target, "failed")
		writeError(c, http.StatusBadRequest, "confirmation_required", "explicit confirmation is required")
		return
	}
	if handler.service == nil {
		handler.recordMutation(c, actorID, action, target, "failed")
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Secret mutation is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.UpdateSecret(ctx, kubernetesresource.UpdateSecretInput{
		ClusterID: c.Param("cluster_id"), Namespace: c.Param("namespace_name"), Name: c.Param("secret_name"),
		UID: request.UID, ResourceVersion: request.ResourceVersion,
		Data: request.Data, Immutable: request.Immutable,
		DryRun: request.DryRun, Confirm: request.Confirm,
		IdempotencyKey: c.GetHeader(idempotencyKeyHeaderName),
	})
	cancel()
	if err != nil {
		handler.recordMutation(c, actorID, action, target, "failed")
	}
	if handler.respondSecretError(c, "update Kubernetes Secret", err) {
		return
	}
	handler.recordMutation(c, actorID, action, target, "succeeded")
	writeSuccess(c, http.StatusOK, gin.H{"resource": result, "dry_run": request.DryRun})
}

func (handler *kubernetesSecretHandler) delete(c *gin.Context) {
	target, actorID, ok := handler.mutationTarget(c, auditaction.KubernetesResourceDelete, c.Param("secret_name"))
	if !ok {
		return
	}
	var request deleteSecretRequest
	if decodeJSONRequest(c, &request, maxKubernetesSecretMutationRequestBytes) != nil {
		handler.recordMutation(c, actorID, auditaction.KubernetesResourceDelete, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid Secret delete request")
		return
	}
	action := kubernetesMutationAuditAction(auditaction.KubernetesResourceDelete, request.DryRun)
	if !request.DryRun && !request.Confirm {
		handler.recordMutation(c, actorID, action, target, "failed")
		writeError(c, http.StatusBadRequest, "confirmation_required", "explicit confirmation is required")
		return
	}
	if handler.service == nil {
		handler.recordMutation(c, actorID, action, target, "failed")
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Secret mutation is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	err := handler.service.DeleteSecret(ctx, kubernetesresource.DeleteSecretInput{
		ClusterID: c.Param("cluster_id"), Namespace: c.Param("namespace_name"), Name: c.Param("secret_name"),
		UID: request.UID, ResourceVersion: request.ResourceVersion,
		DryRun: request.DryRun, Confirm: request.Confirm,
		IdempotencyKey: c.GetHeader(idempotencyKeyHeaderName),
	})
	cancel()
	if err != nil {
		handler.recordMutation(c, actorID, action, target, "failed")
	}
	if handler.respondSecretError(c, "delete Kubernetes Secret", err) {
		return
	}
	handler.recordMutation(c, actorID, action, target, "succeeded")
	writeSuccess(c, http.StatusOK, gin.H{"deleted": !request.DryRun, "dry_run": request.DryRun, "target": target})
}

func (handler *kubernetesSecretHandler) mutationTarget(
	c *gin.Context,
	action string,
	name string,
) (string, string, bool) {
	c.Header("Cache-Control", "no-store")
	actor, _ := httpmiddleware.Identity(c)
	target := handler.target(c, name)
	if len(c.Request.URL.Query()) != 0 {
		handler.recordMutation(c, actor.User.ID, action, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "Secret mutation does not accept query parameters")
		return "", actor.User.ID, false
	}
	return target, actor.User.ID, true
}

func (handler *kubernetesSecretHandler) target(c *gin.Context, name string) string {
	return resourceTargetName(kubernetesresource.SecretResourceIdentity(), c.Param("namespace_name"), name)
}

func (handler *kubernetesSecretHandler) recordMutation(c *gin.Context, actorID, action, target, result string) {
	resourceHandler := kubernetesResourceHandler{baseHandler: handler.baseHandler}
	resourceHandler.recordKubernetesMutation(c, actorID, action, target, result)
}

// recordRead writes the audit record for a Secret read. It takes the same shape
// as a mutation record — actor, Cluster scope, target, result — because a read
// and a write of a Secret are the same question to whoever reviews the trail
// later, and the answer should not be somewhere else.
func (handler *kubernetesSecretHandler) recordRead(c *gin.Context, actorID, action, target, result string) {
	handler.recordOperation(c, auditedOperation{
		Scope:       auditScopeCluster,
		ActorUserID: actorID,
		Action:      action,
		TargetType:  auditaction.TargetKubernetesResource,
		TargetName:  target,
		Result:      result,
	})
}

// readResult maps a read outcome onto the audit vocabulary. A cancelled request
// is recorded as failed rather than dropped: the Server cannot tell a client
// that hung up from one that got its answer and left, and a Secret read is not
// the place to guess in the caller's favour.
func readResult(err error) string {
	if err != nil {
		return "failed"
	}
	return "succeeded"
}

func (handler *kubernetesSecretHandler) respondSecretError(c *gin.Context, operation string, err error) bool {
	if errors.Is(err, kubernetesresource.ErrSecretImmutable) {
		writeError(c, http.StatusConflict, "secret_immutable", "immutable Secret data cannot be changed")
		return true
	}
	resourceHandler := kubernetesResourceHandler{baseHandler: handler.baseHandler}
	return resourceHandler.respondResourceError(c, operation, err)
}

func parseSecretListQuery(query url.Values) (kubernetesresource.ListSecretsInput, error) {
	allowed := map[string]struct{}{"limit": {}, "continue": {}, "label_selector": {}, "field_selector": {}}
	if err := validateQueryNames(query, allowed); err != nil {
		return kubernetesresource.ListSecretsInput{}, err
	}
	result := kubernetesresource.ListSecretsInput{
		Limit: kubernetesresource.DefaultResourceListLimit, ContinueToken: query.Get("continue"),
		LabelSelector: query.Get("label_selector"), FieldSelector: query.Get("field_selector"),
	}
	if value := query.Get("limit"); value != "" {
		limit, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return kubernetesresource.ListSecretsInput{}, errors.New("invalid Secret list limit")
		}
		result.Limit = limit
	}
	return result, nil
}

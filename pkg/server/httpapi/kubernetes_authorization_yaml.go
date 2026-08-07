package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/audit"
	"github.com/togettoyou/zke/pkg/server/auditaction"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
	"github.com/togettoyou/zke/pkg/server/kubernetesyaml"
)

/*
 * YAML for the five Kubernetes authorization families.
 *
 * A route of its own rather than the generic YAML endpoint, because the two
 * differ in the only thing that matters here: this one is gated by
 * `cluster.rbac.read` and `cluster.rbac.manage`. Routing these resources
 * through the generic endpoint would hand every holder of
 * `cluster.resource.update` the ability to rewrite a RoleBinding, which is the
 * reason the generic endpoint refuses them in the first place — and it still
 * does.
 *
 * The target is named by the path rather than by a query, so a caller cannot
 * describe one object in the URL and another in the parameters; the family, its
 * scope and its Namespace are resolved before the request is accepted.
 */
type kubernetesAuthorizationYAMLHandler struct {
	kubernetesYAMLHandler
}

func newKubernetesAuthorizationYAMLHandler(
	logger *slog.Logger,
	service kubernetesYAMLService,
	auditService *audit.Service,
	operationTimeout time.Duration,
) *kubernetesAuthorizationYAMLHandler {
	return &kubernetesAuthorizationYAMLHandler{
		kubernetesYAMLHandler: kubernetesYAMLHandler{
			baseHandler: newBaseHandler(logger, auditService, operationTimeout),
			service:     service,
		},
	}
}

func (handler *kubernetesAuthorizationYAMLHandler) get(c *gin.Context) {
	setYAMLResponseHeaders(c)
	resource, ok := authorizationYAMLTarget(c)
	if !ok || len(c.Request.URL.Query()) != 0 {
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid Kubernetes authorization YAML target")
		return
	}
	if handler.service == nil {
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Kubernetes YAML query is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.Get(ctx, kubernetesyaml.GetInput{
		ClusterID: c.Param("cluster_id"),
		Resource:  resource,
		Namespace: c.Param("namespace_name"),
		Name:      c.Param("authorization_name"),
	})
	cancel()
	if handler.respondYAMLError(c, "get Kubernetes authorization YAML", err) {
		return
	}
	writeYAML(c, result, false)
}

func (handler *kubernetesAuthorizationYAMLHandler) update(c *gin.Context) {
	setYAMLResponseHeaders(c)
	resource, targetOK := authorizationYAMLTarget(c)
	query, err := parseYAMLMutationQuery(c.Request.URL.Query())
	identity, _ := httpmiddleware.Identity(c)
	target := resourceTargetName(
		resource,
		c.Param("namespace_name"),
		c.Param("authorization_name"),
	)
	action := kubernetesMutationAuditAction(
		auditaction.KubernetesResourceUpdate,
		query.DryRun,
	)
	if !targetOK || err != nil {
		handler.recordKubernetesMutation(c, identity.User.ID, action, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid Kubernetes authorization YAML update request")
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
			Resource:  resource,
			Namespace: c.Param("namespace_name"),
			Name:      c.Param("authorization_name"),
		},
		Manifest: manifest,
		// A YAML document can carry a PolicyRule as easily as the form can, so
		// it answers to the same ceiling.
		SecretGrant:    callerSecretGrant(c),
		DryRun:         query.DryRun,
		Confirm:        query.Confirm,
		FieldManager:   query.FieldManager,
		IdempotencyKey: c.GetHeader(idempotencyKeyHeaderName),
	})
	cancel()
	if err != nil {
		handler.recordKubernetesMutation(c, identity.User.ID, action, target, "failed")
	}
	if handler.respondYAMLError(c, "update Kubernetes authorization YAML", err) {
		return
	}
	handler.recordKubernetesMutation(c, identity.User.ID, action, target, "succeeded")
	writeYAML(c, result, query.DryRun)
}

// authorizationYAMLTarget resolves the family named in the path, refusing a
// scope the family does not have.
func authorizationYAMLTarget(c *gin.Context) (kubernetesresource.ResourceIdentity, bool) {
	return kubernetesresource.ScopedAuthorizationResourceIdentity(
		kubernetesresource.AuthorizationResource(c.Param("authorization_resource")),
		c.Param("namespace_name"),
	)
}

// yamlMutationQuery is what a YAML update accepts when the object is named by
// the path: how to apply the change, and nothing about which object it is.
type yamlMutationQuery struct {
	DryRun       bool
	Confirm      bool
	FieldManager string
}

func parseYAMLMutationQuery(query url.Values) (yamlMutationQuery, error) {
	allowed := map[string]struct{}{
		"dry_run": {}, "confirm": {}, "field_manager": {},
	}
	if err := validateQueryNames(query, allowed); err != nil {
		return yamlMutationQuery{}, err
	}
	dryRun, err := parseOptionalBoolean(query.Get("dry_run"))
	if err != nil {
		return yamlMutationQuery{}, err
	}
	confirm, err := parseOptionalBoolean(query.Get("confirm"))
	if err != nil {
		return yamlMutationQuery{}, err
	}
	return yamlMutationQuery{
		DryRun:       dryRun,
		Confirm:      confirm,
		FieldManager: query.Get("field_manager"),
	}, nil
}

// writeYAMLRequestError reports a body this Server would not read at all, which
// is a different failure from a document Kubernetes refused.
func writeYAMLRequestError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, errInvalidYAMLContentType):
		writeError(c, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/yaml")
	case errors.Is(err, errYAMLBodyTooLarge):
		writeError(c, http.StatusRequestEntityTooLarge, "manifest_too_large", "Kubernetes YAML manifest exceeds 4 MiB")
	default:
		writeError(c, http.StatusBadRequest, "invalid_yaml", "invalid Kubernetes YAML manifest")
	}
}

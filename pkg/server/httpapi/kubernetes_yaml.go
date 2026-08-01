package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/audit"
	"github.com/togettoyou/zke/pkg/server/auditaction"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
	"github.com/togettoyou/zke/pkg/server/kubernetesyaml"
)

const (
	kubernetesYAMLContentType = "application/yaml"
	maxKubernetesYAMLBytes    = 4 * 1024 * 1024
)

var (
	errInvalidYAMLContentType = errors.New("invalid Kubernetes YAML content type")
	errYAMLBodyTooLarge       = errors.New("Kubernetes YAML body is too large")
)

type kubernetesYAMLService interface {
	Get(context.Context, kubernetesyaml.GetInput) (kubernetesyaml.Result, error)
	Update(context.Context, kubernetesyaml.UpdateInput) (kubernetesyaml.Result, error)
}

type kubernetesYAMLHandler struct {
	baseHandler
	service kubernetesYAMLService
}

type kubernetesYAMLUpdateQuery struct {
	Resource     kubernetesresource.ResourceIdentity
	Namespace    string
	DryRun       bool
	Confirm      bool
	FieldManager string
}

func newKubernetesYAMLHandler(
	logger *slog.Logger,
	service kubernetesYAMLService,
	auditService *audit.Service,
	operationTimeout time.Duration,
) *kubernetesYAMLHandler {
	return &kubernetesYAMLHandler{
		baseHandler: newBaseHandler(logger, auditService, operationTimeout),
		service:     service,
	}
}

func (handler *kubernetesYAMLHandler) get(c *gin.Context) {
	setYAMLResponseHeaders(c)
	resource, namespace, err := parseGenericResourceIdentityQuery(
		c.Request.URL.Query(),
	)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid Kubernetes YAML resource query")
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
		Namespace: namespace,
		Name:      c.Param("resource_name"),
	})
	cancel()
	if handler.respondYAMLError(c, "get Kubernetes resource YAML", err) {
		return
	}
	writeYAML(c, result, false)
}

func (handler *kubernetesYAMLHandler) update(c *gin.Context) {
	setYAMLResponseHeaders(c)
	query, err := parseKubernetesYAMLUpdateQuery(c.Request.URL.Query())
	identity, _ := httpmiddleware.Identity(c)
	targetName := resourceTargetName(
		query.Resource,
		query.Namespace,
		c.Param("resource_name"),
	)
	action := kubernetesMutationAuditAction(
		auditaction.KubernetesResourceUpdate,
		query.DryRun,
	)
	if err != nil {
		handler.recordKubernetesMutation(c, identity.User.ID, action, targetName, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid Kubernetes YAML update query")
		return
	}
	if !query.DryRun && !query.Confirm {
		handler.recordKubernetesMutation(c, identity.User.ID, action, targetName, "failed")
		writeError(c, http.StatusBadRequest, "confirmation_required", "explicit confirmation is required")
		return
	}
	manifest, err := readYAMLManifest(c)
	if err != nil {
		handler.recordKubernetesMutation(c, identity.User.ID, action, targetName, "failed")
		switch {
		case errors.Is(err, errInvalidYAMLContentType):
			writeError(c, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/yaml")
		case errors.Is(err, errYAMLBodyTooLarge):
			writeError(c, http.StatusRequestEntityTooLarge, "manifest_too_large", "Kubernetes YAML manifest exceeds 4 MiB")
		default:
			writeError(c, http.StatusBadRequest, "invalid_yaml", "invalid Kubernetes YAML manifest")
		}
		return
	}
	if handler.service == nil {
		handler.recordKubernetesMutation(c, identity.User.ID, action, targetName, "failed")
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Kubernetes YAML mutation is unavailable")
		return
	}

	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.Update(ctx, kubernetesyaml.UpdateInput{
		GetInput: kubernetesyaml.GetInput{
			ClusterID: c.Param("cluster_id"),
			Resource:  query.Resource,
			Namespace: query.Namespace,
			Name:      c.Param("resource_name"),
		},
		Manifest:       manifest,
		DryRun:         query.DryRun,
		Confirm:        query.Confirm,
		FieldManager:   query.FieldManager,
		IdempotencyKey: c.GetHeader(idempotencyKeyHeaderName),
	})
	cancel()
	if err != nil {
		handler.recordKubernetesMutation(c, identity.User.ID, action, targetName, "failed")
	}
	if handler.respondYAMLError(c, "update Kubernetes resource YAML", err) {
		return
	}
	handler.recordKubernetesMutation(c, identity.User.ID, action, targetName, "succeeded")
	writeYAML(c, result, query.DryRun)
}

func parseKubernetesYAMLUpdateQuery(
	query url.Values,
) (kubernetesYAMLUpdateQuery, error) {
	allowed := map[string]struct{}{
		"group": {}, "version": {}, "resource": {}, "namespace": {},
		"dry_run": {}, "confirm": {}, "field_manager": {},
	}
	if err := validateQueryNames(query, allowed); err != nil {
		return kubernetesYAMLUpdateQuery{}, err
	}
	resource, namespace, err := genericResourceIdentity(query)
	if err != nil {
		return kubernetesYAMLUpdateQuery{}, err
	}
	dryRun, err := parseOptionalBoolean(query.Get("dry_run"))
	if err != nil {
		return kubernetesYAMLUpdateQuery{}, err
	}
	confirm, err := parseOptionalBoolean(query.Get("confirm"))
	if err != nil {
		return kubernetesYAMLUpdateQuery{}, err
	}
	return kubernetesYAMLUpdateQuery{
		Resource:     resource,
		Namespace:    namespace,
		DryRun:       dryRun,
		Confirm:      confirm,
		FieldManager: query.Get("field_manager"),
	}, nil
}

func parseOptionalBoolean(value string) (bool, error) {
	if value == "" {
		return false, nil
	}
	if value != "true" && value != "false" {
		return false, errors.New("boolean query parameter must be true or false")
	}
	return strconv.ParseBool(value)
}

func readYAMLManifest(c *gin.Context) ([]byte, error) {
	if c.GetHeader("Content-Encoding") != "" {
		return nil, errInvalidYAMLContentType
	}
	mediaType, parameters, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil || mediaType != kubernetesYAMLContentType {
		return nil, errInvalidYAMLContentType
	}
	for name, value := range parameters {
		if name != "charset" || !strings.EqualFold(value, "utf-8") {
			return nil, errInvalidYAMLContentType
		}
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxKubernetesYAMLBytes)
	manifest, err := io.ReadAll(c.Request.Body)
	var maximumError *http.MaxBytesError
	if errors.As(err, &maximumError) {
		return nil, errYAMLBodyTooLarge
	}
	if err != nil {
		return nil, err
	}
	return manifest, nil
}

func setYAMLResponseHeaders(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("X-Content-Type-Options", "nosniff")
}

func writeYAML(c *gin.Context, result kubernetesyaml.Result, dryRun bool) {
	c.Header("X-ZKE-Dry-Run", strconv.FormatBool(dryRun))
	c.Header("X-ZKE-Resource-UID", result.UID)
	c.Header("X-ZKE-Resource-Version", result.ResourceVersion)
	c.Data(http.StatusOK, kubernetesYAMLContentType+"; charset=utf-8", result.Manifest)
}

func (handler *kubernetesYAMLHandler) respondYAMLError(
	c *gin.Context,
	operation string,
	err error,
) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, kubernetesresource.ErrRequestCapacity) ||
		errors.Is(err, kubernetesresource.ErrResponseBudget) {
		c.Header("Retry-After", "1")
	}
	mappings := []errorMapping{
		{kubernetesyaml.ErrInvalidManifest, http.StatusBadRequest, "invalid_yaml", "invalid Kubernetes YAML manifest"},
		{kubernetesyaml.ErrResourceUIDChanged, http.StatusConflict, "resource_uid_changed", "Kubernetes resource was deleted and recreated"},
		{kubernetesyaml.ErrResourceVersionChanged, http.StatusConflict, "resource_version_changed", "Kubernetes resource changed after the YAML was loaded"},
	}
	mappings = append(mappings, kubernetesResourceErrorMappings()...)
	return handler.respondError(c, operation, err, mappings...)
}

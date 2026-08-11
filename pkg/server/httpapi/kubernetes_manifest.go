package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/audit"
	"github.com/togettoyou/zke/pkg/server/auditaction"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
	"github.com/togettoyou/zke/pkg/server/kubernetesmanifest"
	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
	"github.com/togettoyou/zke/pkg/server/rbac"
)

// KubernetesManifestHTTPConfig bounds a manifest request.
//
// The whole-request timeout is its own setting because a manifest is the one
// endpoint whose cost is not fixed by its route: every document is a separate
// round trip to the Agent, so the ten seconds that bound a single-object write
// would refuse a thirty-object file that was going to succeed. Each individual
// document still runs under the ordinary operation timeout, so one unresponsive
// object cannot consume the whole budget.
type KubernetesManifestHTTPConfig struct {
	RequestTimeout time.Duration
	MaxDocuments   int
}

// The field manager server-side Apply records ownership under.
//
// One fixed value for the whole platform, and not a client-supplied one: Apply
// converges on repeated runs only while the manager stays the same, and it is
// also what tells an operator reading `managedFields` that ZKE's manifest
// endpoint owns a field rather than the object's controller or a person's
// kubectl.
const manifestFieldManager = "zke-manifest"

type kubernetesManifestService interface {
	Execute(
		context.Context,
		kubernetesmanifest.ResourceAccess,
		kubernetesmanifest.Input,
	) (kubernetesmanifest.Result, error)
}

type kubernetesManifestHandler struct {
	baseHandler
	service   kubernetesManifestService
	resources *kubernetesresource.Service
	config    KubernetesManifestHTTPConfig
}

func newKubernetesManifestHandler(
	logger *slog.Logger,
	service kubernetesManifestService,
	resources *kubernetesresource.Service,
	auditService *audit.Service,
	operationTimeout time.Duration,
	config KubernetesManifestHTTPConfig,
) *kubernetesManifestHandler {
	return &kubernetesManifestHandler{
		baseHandler: newBaseHandler(logger, auditService, operationTimeout),
		service:     service,
		resources:   resources,
		config:      config,
	}
}

type kubernetesManifestQuery struct {
	Namespace string
	DryRun    bool
	Confirm   bool
	Force     bool
}

func (handler *kubernetesManifestHandler) apply(c *gin.Context) {
	handler.run(c, kubernetesmanifest.OperationApply)
}

func (handler *kubernetesManifestHandler) delete(c *gin.Context) {
	handler.run(c, kubernetesmanifest.OperationDelete)
}

func (handler *kubernetesManifestHandler) run(
	c *gin.Context,
	operation kubernetesmanifest.Operation,
) {
	c.Header("Cache-Control", "no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	identity, _ := httpmiddleware.Identity(c)
	query, err := parseKubernetesManifestQuery(c.Request.URL.Query(), operation)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid Kubernetes manifest query")
		return
	}
	if !query.DryRun && !query.Confirm {
		writeError(c, http.StatusBadRequest, "confirmation_required", "explicit confirmation is required")
		return
	}
	manifest, err := readYAMLManifest(c)
	if err != nil {
		writeYAMLRequestError(c, err)
		return
	}
	if handler.service == nil || handler.resources == nil {
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Kubernetes manifest operations are unavailable")
		return
	}
	idempotencyKey := c.GetHeader(idempotencyKeyHeaderName)

	// The grant the middleware resolved, handed to the resource layer as the one
	// thing that decides which families this request may touch. Built per request
	// and never cached: it is the caller's permissions, not the Cluster's.
	access := kubernetesresource.NewManifestAccess(
		handler.resources,
		manifestGrant(httpmiddleware.ClusterManifestGrant(c)),
	)

	ctx, cancel := context.WithTimeout(
		c.Request.Context(),
		handler.config.RequestTimeout,
	)
	result, err := handler.service.Execute(ctx, access, kubernetesmanifest.Input{
		ClusterID:      c.Param("cluster_id"),
		Manifest:       manifest,
		Namespace:      query.Namespace,
		Operation:      operation,
		DryRun:         query.DryRun,
		Force:          query.Force,
		Confirm:        query.Confirm,
		IdempotencyKey: idempotencyKey,
	})
	cancel()
	// A whole-request failure — an unparseable manifest, a Cluster that could not
	// be reached for discovery — has no document to hang a record on, and nothing
	// was written. Anything one document ran into is on that document instead,
	// and is recorded below.
	if handler.respondManifestError(c, "execute Kubernetes manifest", err) {
		return
	}

	handler.recordDocuments(c, identity.User.ID, operation, query.DryRun, result)
	// A dry run always answers with the per-document verdicts, even when they are
	// all refusals. Reporting it as an error instead would be withholding the one
	// thing the dry run exists to produce: which document is the problem and which
	// permission it needs. Nothing was written either way — that is what makes the
	// difference safe to draw here.
	//
	// An execution says so with a status, because a caller who asked to change the
	// Cluster and changed nothing must not have to read a 200 body carefully to
	// find that out. The malformed document is reported before the refused one
	// when both are present: it is about what the caller sent, and it is the one
	// they can fix without asking anybody.
	if !query.DryRun {
		if !result.Valid {
			writeError(c, http.StatusBadRequest, "invalid_document", manifestInvalidMessage)
			return
		}
		if !result.Allowed {
			writeError(c, http.StatusForbidden, "forbidden", manifestRefusalMessage)
			return
		}
	}
	writeSuccess(c, http.StatusOK, manifestResponse(result))
}

// parseKubernetesManifestQuery reads the request's own options. Nothing about
// which objects are touched comes from here — that is the manifest's job — so
// the set is small and closed.
func parseKubernetesManifestQuery(
	query url.Values,
	operation kubernetesmanifest.Operation,
) (kubernetesManifestQuery, error) {
	allowed := map[string]struct{}{
		"namespace": {}, "dry_run": {}, "confirm": {},
	}
	if operation == kubernetesmanifest.OperationApply {
		allowed["force"] = struct{}{}
	}
	if err := validateQueryNames(query, allowed); err != nil {
		return kubernetesManifestQuery{}, err
	}
	dryRun, err := parseOptionalBoolean(query.Get("dry_run"))
	if err != nil {
		return kubernetesManifestQuery{}, err
	}
	confirm, err := parseOptionalBoolean(query.Get("confirm"))
	if err != nil {
		return kubernetesManifestQuery{}, err
	}
	force, err := parseOptionalBoolean(query.Get("force"))
	if err != nil {
		return kubernetesManifestQuery{}, err
	}
	return kubernetesManifestQuery{
		Namespace: query.Get("namespace"),
		DryRun:    dryRun,
		Confirm:   confirm,
		Force:     force,
	}, nil
}

func manifestGrant(grant httpmiddleware.ManifestGrant) kubernetesresource.ManifestGrant {
	return kubernetesresource.ManifestGrant{
		ResourceCreate:        grant.ResourceCreate,
		ResourceUpdate:        grant.ResourceUpdate,
		ResourceDelete:        grant.ResourceDelete,
		NamespaceManage:       grant.NamespaceManage,
		SecretRead:            grant.SecretRead,
		SecretManage:          grant.SecretManage,
		RBACManage:            grant.RBACManage,
		SystemNamespaceManage: grant.SystemNamespaceManage,
		AgentNamespaceManage:  grant.AgentNamespaceManage,
	}
}

// manifestRequirementPermission names the permission a document answers to.
//
// The resource layer decides which requirement applies but does not know what it
// is called, because it must not depend on `pkg/server/rbac`. This is the only
// place the two vocabularies meet, which is what keeps the permission names
// declared once. TestManifestRequirementsArePublished holds it to being total.
func manifestRequirementPermission(
	requirement kubernetesresource.ManifestRequirement,
) string {
	switch requirement {
	case kubernetesresource.ManifestRequirementResourceCreate:
		return string(rbac.PermissionClusterResourceCreate)
	case kubernetesresource.ManifestRequirementResourceUpdate:
		return string(rbac.PermissionClusterResourceUpdate)
	case kubernetesresource.ManifestRequirementResourceDelete:
		return string(rbac.PermissionClusterResourceDelete)
	case kubernetesresource.ManifestRequirementNamespaceManage:
		return string(rbac.PermissionClusterNamespaceManage)
	case kubernetesresource.ManifestRequirementSecretManage:
		return string(rbac.PermissionClusterSecretManage)
	case kubernetesresource.ManifestRequirementRBACManage:
		return string(rbac.PermissionClusterRBACManage)
	case kubernetesresource.ManifestRequirementSystemNamespaceManage:
		return string(rbac.PermissionClusterSystemNamespaceManage)
	case kubernetesresource.ManifestRequirementAgentNamespaceManage:
		return string(rbac.PermissionClusterAgentNamespaceManage)
	default:
		return ""
	}
}

func manifestResponse(result kubernetesmanifest.Result) gin.H {
	documents := make([]gin.H, 0, len(result.Documents))
	for _, document := range result.Documents {
		code, message := manifestDocumentError(document.Err)
		documents = append(documents, gin.H{
			"index":            document.Index,
			"api_version":      document.APIVersion,
			"kind":             document.Kind,
			"namespace":        document.Namespace,
			"name":             document.Name,
			"action":           string(document.Action),
			"status":           string(document.Status),
			"previewed":        document.Previewed,
			"permission":       manifestRequirementPermission(document.Requirement),
			"uid":              document.UID,
			"resource_version": document.ResourceVersion,
			"error_code":       code,
			"error_message":    message,
		})
	}
	return gin.H{
		"dry_run":         result.DryRun,
		"allowed":         result.Allowed,
		"valid":           result.Valid,
		"failed":          result.Failed,
		"catalog_partial": result.CatalogPartial,
		"documents":       documents,
	}
}

// manifestDocumentError maps one document's failure to the code and message the
// rest of the Kubernetes API already uses for it.
//
// Reusing the shared mappings rather than writing new prose: an operator who has
// seen `cluster_api_rejected` on a single-object save must not meet a second
// name for the same rejection here. Anything unmapped becomes a generic code
// with no detail — a document's failure must never carry the Server's internals
// into a response.
func manifestDocumentError(err error) (string, string) {
	if err == nil {
		return "", ""
	}
	mappings := append(
		[]errorMapping{
			{kubernetesresource.ErrManifestForbidden, http.StatusForbidden, "forbidden", "permission denied for this document"},
			{kubernetesresource.ErrManifestResourceRefused, http.StatusBadRequest, "resource_refused", "this Kubernetes resource cannot be written from a manifest"},
			{kubernetesmanifest.ErrUnknownKind, http.StatusBadRequest, "unknown_kind", "the Cluster does not serve this Kind"},
			{kubernetesmanifest.ErrVerbUnsupported, http.StatusBadRequest, "verb_unsupported", "the Cluster does not support this operation on this resource"},
			{kubernetesmanifest.ErrDuplicateDocument, http.StatusBadRequest, "duplicate_document", "another document in this manifest names the same object"},
			{kubernetesmanifest.ErrDocumentInvalid, http.StatusBadRequest, "invalid_document", "the document does not name a complete Kubernetes object"},
			{kubernetesresource.ErrPlatformLabelClaimed, http.StatusBadRequest, "platform_label_forbidden", "objects cannot claim the ZKE managed-by label"},
			{kubernetesresource.ErrRoleRefImmutable, http.StatusConflict, "role_ref_immutable", "roleRef cannot be changed; delete the binding and create it again"},
			{kubernetesresource.ErrSecretImmutable, http.StatusConflict, "secret_immutable", "immutable Secret data cannot be changed"},
			{kubernetesresource.ErrSecretTypeImmutable, http.StatusConflict, "secret_type_immutable", "Secret type is fixed at creation"},
		},
		kubernetesResourceErrorMappings()...,
	)
	for _, mapping := range mappings {
		if !errors.Is(err, mapping.target) {
			continue
		}
		message := mapping.message
		var detailed detailedError
		if errors.As(err, &detailed) && detailed.Detail() != "" {
			message = detailed.Detail()
		}
		return mapping.code, message
	}
	return "internal_error", "the document could not be processed"
}

// The messages name no object: which documents are affected, and why, is what a
// dry run reports per document, and a list of objects in an error string makes it
// long without making it actionable.
const (
	manifestRefusalMessage = "permission denied for documents in this manifest; run a dry run to see which ones"
	manifestInvalidMessage = "this manifest holds a document that cannot be applied; run a dry run to see which one. Nothing was written."
)

// recordDocuments writes the audit trail for one manifest request.
//
// The rule is what changed in the Cluster: a request that changed nothing gets
// one record, and a request that changed objects gets one per object.
//
// A dry run and a refused request both changed nothing, and both are things an
// operator does repeatedly — a dry run while correcting a file, a refusal until
// somebody grants the missing permission. One record per document there would
// put sixty rows in the trail for every preview click and bury the records that
// name real writes among them. The aggregate still says who, which Cluster, which
// operation, how many documents and how many were refused, which is everything
// the event actually holds.
//
// An execution stays per document, because objects were changed and the trail
// has to name each one. Documents that were never sent — invalid, refused, or
// after the failure that stopped the run — are recorded as failures under the
// action they would have performed: an attempt that was stopped partway is
// exactly what the trail exists to show.
func (handler *kubernetesManifestHandler) recordDocuments(
	c *gin.Context,
	actorUserID string,
	operation kubernetesmanifest.Operation,
	dryRun bool,
	result kubernetesmanifest.Result,
) {
	for _, record := range manifestAuditRecords(operation, dryRun, result) {
		handler.recordKubernetesMutation(
			c,
			actorUserID,
			record.Action,
			record.TargetName,
			record.Result,
		)
	}
}

// manifestAuditRecord is one row the trail is about to receive.
type manifestAuditRecord struct {
	Action     string
	TargetName string
	Result     string
}

// manifestAuditRecords decides what a request writes to the trail. Split out from
// the write so the rule above can be checked without a database behind it.
func manifestAuditRecords(
	operation kubernetesmanifest.Operation,
	dryRun bool,
	result kubernetesmanifest.Result,
) []manifestAuditRecord {
	action := kubernetesMutationAuditAction(manifestAuditAction(operation), dryRun)
	if dryRun || !result.Executable() {
		outcome := "failed"
		if result.Executable() && !result.Failed {
			outcome = "succeeded"
		}
		return []manifestAuditRecord{{
			Action:     action,
			TargetName: manifestRequestTargetName(operation, result),
			Result:     outcome,
		}}
	}
	records := make([]manifestAuditRecord, 0, len(result.Documents))
	for _, document := range result.Documents {
		if document.Status == kubernetesmanifest.StatusSkipped {
			// Nothing was asked of the Cluster and nothing changed: a delete
			// naming an object that is already gone.
			continue
		}
		outcome := "failed"
		if document.Status == kubernetesmanifest.StatusSucceeded {
			outcome = "succeeded"
		}
		records = append(records, manifestAuditRecord{
			Action:     action,
			TargetName: manifestTargetName(document),
			Result:     outcome,
		})
	}
	return records
}

// manifestRequestTargetName names the manifest itself, for the records that are
// about a request rather than about an object.
//
// It never names the objects: a target naming sixty of them is a target nobody
// reads, and the per-document records are where those names belong. The refused
// count is included because it is the one number that changes what the record
// means — a refusal is a permission boundary having been reached, not a request
// that merely did nothing.
func manifestRequestTargetName(
	operation kubernetesmanifest.Operation,
	result kubernetesmanifest.Result,
) string {
	refused, invalid := 0, 0
	for _, document := range result.Documents {
		switch document.Status {
		case kubernetesmanifest.StatusRefused:
			refused++
		case kubernetesmanifest.StatusInvalid:
			invalid++
		}
	}
	target := fmt.Sprintf(
		"manifest/%s %d documents",
		operation,
		len(result.Documents),
	)
	if refused > 0 {
		target += fmt.Sprintf(", %d refused", refused)
	}
	if invalid > 0 {
		target += fmt.Sprintf(", %d invalid", invalid)
	}
	return target
}

func manifestAuditAction(operation kubernetesmanifest.Operation) string {
	if operation == kubernetesmanifest.OperationDelete {
		return auditaction.KubernetesResourceDelete
	}
	// Apply is a patch. Recording it as one keeps the action vocabulary closed
	// rather than adding a name for a write that already has one.
	return auditaction.KubernetesResourcePatch
}

// manifestTargetName names the object a record is about, from the document
// rather than from a resolved GVR: a document too malformed to resolve still has
// to be recorded, and what it said about itself is what there is to record.
func manifestTargetName(document kubernetesmanifest.Document) string {
	target := document.APIVersion + "/" + document.Kind
	if document.Namespace != "" {
		target += " " + document.Namespace
	}
	if document.Name != "" {
		target += "/" + document.Name
	}
	return target
}

func (handler *kubernetesManifestHandler) respondManifestError(
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
		{kubernetesmanifest.ErrInvalidManifest, http.StatusBadRequest, "invalid_yaml", "invalid Kubernetes YAML manifest"},
		{kubernetesmanifest.ErrEmptyManifest, http.StatusBadRequest, "empty_manifest", "the manifest holds no Kubernetes documents"},
		{kubernetesmanifest.ErrTooManyDocuments, http.StatusBadRequest, "too_many_documents", "the manifest holds more documents than one request accepts"},
	}
	mappings = append(mappings, kubernetesResourceErrorMappings()...)
	return handler.respondError(c, operation, err, mappings...)
}

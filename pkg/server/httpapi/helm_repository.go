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
	"github.com/togettoyou/zke/pkg/server/helm"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
)

// The chart catalogue.
//
// Two audiences, two permissions. A platform administrator holding
// `helm.repository.manage` decides which repositories exist; anyone holding
// `helm.repository.read` may browse what they publish, because choosing a chart
// is the first half of installing one and the second half is gated on the
// Cluster.
//
// Reading a chart makes this Server fetch from an address an administrator
// configured. That is why the catalogue is curated rather than open: without
// it, "install this chart" would be a way to make the Server issue a request to
// any address the caller named.
//
// A repository credential is write-only. It is stored, it is sent upstream, and
// no route returns it — a reader is told only whether one is set.

const maxHelmRepositoryRequestBytes = 128 * 1024

type helmRepositoryService interface {
	ListRepositories(context.Context) (helm.RepositoryPage, error)
	GetRepository(context.Context, string) (helm.Repository, error)
	CreateRepository(context.Context, helm.RepositoryInput, string) (helm.Repository, error)
	UpdateRepository(context.Context, string, helm.RepositoryInput, string) (helm.Repository, error)
	DeleteRepository(context.Context, string) error
	ListCharts(context.Context, string, string, int) (helm.ChartPage, error)
	RefreshCharts(context.Context, string, string, int) (helm.ChartPage, error)
	ListChartVersions(context.Context, string, string) (helm.ChartVersionPage, error)
	GetChart(context.Context, string, string, string) (helm.ChartDetail, error)
	ListChartFiles(context.Context, string, string, string) (helm.ChartFilePage, error)
	GetChartFile(context.Context, string, string, string, string) (helm.ChartFileDetail, error)
}

// helmRepositoryServiceOrNil keeps a nil *helm.Service from becoming a non-nil
// interface holding a nil pointer, which would pass the handler's readiness
// check and then panic on the first call.
func helmRepositoryServiceOrNil(service *helm.Service) helmRepositoryService {
	if service == nil {
		return nil
	}
	return service
}

type helmRepositoryHandler struct {
	baseHandler
	service helmRepositoryService
}

func newHelmRepositoryHandler(
	logger *slog.Logger,
	service helmRepositoryService,
	auditService *audit.Service,
	operationTimeout time.Duration,
) *helmRepositoryHandler {
	return &helmRepositoryHandler{
		baseHandler: newBaseHandler(logger, auditService, operationTimeout),
		service:     service,
	}
}

type helmRepositoryRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	URL         string `json:"url"`
	Username    string `json:"username"`
	// Password is three-state on update: absent keeps the stored credential,
	// an empty string clears it, a value replaces it. A Console that is never
	// shown the password can therefore save the rest of the row.
	Password              *string `json:"password"`
	CACertificatePEM      string  `json:"ca_certificate_pem"`
	InsecureSkipTLSVerify bool    `json:"insecure_skip_tls_verify"`
	Enabled               *bool   `json:"enabled"`
	// SignaturePolicy and PublicKeyring say what this repository's charts must
	// be signed with. Unlike the password these are sent and returned in full —
	// they are public keys, and an administrator adding one to a keyring of
	// three has to submit the whole of it.
	SignaturePolicy string `json:"signature_policy"`
	PublicKeyring   string `json:"public_keyring"`
}

func (request helmRepositoryRequest) input() helm.RepositoryInput {
	return helm.RepositoryInput{
		Name:                  request.Name,
		Description:           request.Description,
		URL:                   request.URL,
		Username:              request.Username,
		Password:              request.Password,
		CACertificatePEM:      request.CACertificatePEM,
		InsecureSkipTLSVerify: request.InsecureSkipTLSVerify,
		Enabled:               request.Enabled,
		SignaturePolicy:       request.SignaturePolicy,
		PublicKeyring:         request.PublicKeyring,
	}
}

func (handler *helmRepositoryHandler) list(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !handler.ready(c, "Helm chart repository query is unavailable") {
		return
	}
	if len(c.Request.URL.Query()) != 0 {
		writeError(c, http.StatusBadRequest, "invalid_request",
			"Helm chart repository list does not accept query parameters")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.ListRepositories(ctx)
	cancel()
	if handler.respondRepositoryError(c, "list Helm chart repositories", err) {
		return
	}
	writeSuccess(c, http.StatusOK, result)
}

func (handler *helmRepositoryHandler) get(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !handler.ready(c, "Helm chart repository query is unavailable") {
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.GetRepository(ctx, c.Param("repository_id"))
	cancel()
	if handler.respondRepositoryError(c, "get Helm chart repository", err) {
		return
	}
	writeSuccess(c, http.StatusOK, result)
}

func (handler *helmRepositoryHandler) create(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	actor, _ := httpmiddleware.Identity(c)
	var request helmRepositoryRequest
	if decodeJSONRequest(c, &request, maxHelmRepositoryRequestBytes) != nil {
		handler.record(c, actor.User.ID, auditaction.HelmRepositoryCreate, "", request.Name, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid Helm chart repository request")
		return
	}
	if !handler.ready(c, "Helm chart repository management is unavailable") {
		handler.record(c, actor.User.ID, auditaction.HelmRepositoryCreate, "", request.Name, "failed")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.CreateRepository(ctx, request.input(), actor.User.ID)
	cancel()
	if err != nil {
		handler.record(c, actor.User.ID, auditaction.HelmRepositoryCreate, "", request.Name, "failed")
	}
	if handler.respondRepositoryError(c, "create Helm chart repository", err) {
		return
	}
	handler.record(c, actor.User.ID, auditaction.HelmRepositoryCreate, result.ID, result.Name, "succeeded")
	writeSuccess(c, http.StatusCreated, result)
}

func (handler *helmRepositoryHandler) update(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	actor, _ := httpmiddleware.Identity(c)
	id := c.Param("repository_id")
	var request helmRepositoryRequest
	if decodeJSONRequest(c, &request, maxHelmRepositoryRequestBytes) != nil {
		handler.record(c, actor.User.ID, auditaction.HelmRepositoryUpdate, id, request.Name, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid Helm chart repository request")
		return
	}
	if !handler.ready(c, "Helm chart repository management is unavailable") {
		handler.record(c, actor.User.ID, auditaction.HelmRepositoryUpdate, id, request.Name, "failed")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.UpdateRepository(ctx, id, request.input(), actor.User.ID)
	cancel()
	if err != nil {
		handler.record(c, actor.User.ID, auditaction.HelmRepositoryUpdate, id, request.Name, "failed")
	}
	if handler.respondRepositoryError(c, "update Helm chart repository", err) {
		return
	}
	handler.record(c, actor.User.ID, auditaction.HelmRepositoryUpdate, result.ID, result.Name, "succeeded")
	writeSuccess(c, http.StatusOK, result)
}

func (handler *helmRepositoryHandler) delete(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	actor, _ := httpmiddleware.Identity(c)
	id := c.Param("repository_id")
	if !handler.ready(c, "Helm chart repository management is unavailable") {
		handler.record(c, actor.User.ID, auditaction.HelmRepositoryDelete, id, "", "failed")
		return
	}
	ctx, cancel := handler.operationContext(c)
	err := handler.service.DeleteRepository(ctx, id)
	cancel()
	if err != nil {
		handler.record(c, actor.User.ID, auditaction.HelmRepositoryDelete, id, "", "failed")
	}
	if handler.respondRepositoryError(c, "delete Helm chart repository", err) {
		return
	}
	handler.record(c, actor.User.ID, auditaction.HelmRepositoryDelete, id, "", "succeeded")
	c.Status(http.StatusNoContent)
}

// charts browses one repository's index. It is a read of an upstream document
// this Server caches, so it is not audited: nothing about a Cluster is exposed,
// and recording every keystroke of a search box would bury the events that
// matter.
func (handler *helmRepositoryHandler) charts(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !handler.ready(c, "Helm chart catalogue is unavailable") {
		return
	}
	query := c.Request.URL.Query()
	if err := validateQueryNames(query, map[string]struct{}{
		"search": {}, "limit": {},
	}); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid chart query")
		return
	}
	limit, err := parseChartLimit(query)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid chart query limit")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.ListCharts(
		ctx,
		c.Param("repository_id"),
		query.Get("search"),
		limit,
	)
	cancel()
	if handler.respondRepositoryError(c, "list Helm charts", err) {
		return
	}
	writeSuccess(c, http.StatusOK, result)
}

// refreshCharts re-reads the repository index before listing.
//
// A POST rather than a flag on the listing: the answer to "is this catalogue
// current" is a property of the Server's cache, and asking it to go and look
// again is an action an operator takes deliberately — not something a page
// refresh should do on every render. It changes no stored state, so it is not
// audited; what it costs is one request to the repository.
func (handler *helmRepositoryHandler) refreshCharts(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !handler.ready(c, "Helm chart catalogue is unavailable") {
		return
	}
	query := c.Request.URL.Query()
	if err := validateQueryNames(query, map[string]struct{}{
		"search": {}, "limit": {},
	}); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid chart query")
		return
	}
	limit, err := parseChartLimit(query)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid chart query limit")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.RefreshCharts(
		ctx,
		c.Param("repository_id"),
		query.Get("search"),
		limit,
	)
	cancel()
	if handler.respondRepositoryError(c, "refresh Helm chart index", err) {
		return
	}
	writeSuccess(c, http.StatusOK, result)
}

func (handler *helmRepositoryHandler) chartVersions(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !handler.ready(c, "Helm chart catalogue is unavailable") {
		return
	}
	if len(c.Request.URL.Query()) != 0 {
		writeError(c, http.StatusBadRequest, "invalid_request",
			"chart version list does not accept query parameters")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.ListChartVersions(
		ctx,
		c.Param("repository_id"),
		c.Param("chart_name"),
	)
	cancel()
	if handler.respondRepositoryError(c, "list Helm chart versions", err) {
		return
	}
	writeSuccess(c, http.StatusOK, result)
}

// chart downloads one chart version and returns what an operator reads before
// installing: the chart's own values.yaml, its README and its metadata.
func (handler *helmRepositoryHandler) chart(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !handler.ready(c, "Helm chart catalogue is unavailable") {
		return
	}
	query := c.Request.URL.Query()
	if err := validateQueryNames(query, map[string]struct{}{"version": {}}); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid chart query")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.GetChart(
		ctx,
		c.Param("repository_id"),
		c.Param("chart_name"),
		query.Get("version"),
	)
	cancel()
	if handler.respondRepositoryError(c, "get Helm chart", err) {
		return
	}
	writeSuccess(c, http.StatusOK, result)
}

// chartFiles lists what one chart archive holds.
//
// A request of its own rather than part of the chart detail: most readers of a
// chart open the README and never the tree, and deciding what several hundred
// archive members are is not work that request should be doing for them. The
// Server holds the parsed archive for a few minutes, so opening the browser
// after reading the detail costs no second download.
func (handler *helmRepositoryHandler) chartFiles(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !handler.ready(c, "Helm chart catalogue is unavailable") {
		return
	}
	query := c.Request.URL.Query()
	if err := validateQueryNames(query, map[string]struct{}{"version": {}}); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid chart file list query")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.ListChartFiles(
		ctx,
		c.Param("repository_id"),
		c.Param("chart_name"),
		query.Get("version"),
	)
	cancel()
	if handler.respondRepositoryError(c, "list Helm chart files", err) {
		return
	}
	writeSuccess(c, http.StatusOK, result)
}

// chartFile returns one file out of a chart archive.
//
// A request of its own rather than part of the listing: a chart with a packaged
// subchart carries hundreds of files that nobody opens. The path is matched
// against the archive's own member names, so there is nothing here for a caller
// to traverse with.
func (handler *helmRepositoryHandler) chartFile(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !handler.ready(c, "Helm chart catalogue is unavailable") {
		return
	}
	query := c.Request.URL.Query()
	if err := validateQueryNames(query, map[string]struct{}{
		"version": {}, "path": {},
	}); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid chart file query")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.GetChartFile(
		ctx,
		c.Param("repository_id"),
		c.Param("chart_name"),
		query.Get("version"),
		query.Get("path"),
	)
	cancel()
	if handler.respondRepositoryError(c, "get Helm chart file", err) {
		return
	}
	writeSuccess(c, http.StatusOK, result)
}

func parseChartLimit(query url.Values) (int, error) {
	value := query.Get("limit")
	if value == "" {
		return 0, nil
	}
	limit, err := strconv.Atoi(value)
	if err != nil || limit <= 0 {
		return 0, errors.New("invalid limit")
	}
	return limit, nil
}

func (handler *helmRepositoryHandler) ready(c *gin.Context, message string) bool {
	if handler.service != nil {
		return true
	}
	writeError(c, http.StatusServiceUnavailable, "unavailable", message)
	return false
}

func (handler *helmRepositoryHandler) record(
	c *gin.Context,
	actorID string,
	action string,
	targetID string,
	targetName string,
	result string,
) {
	handler.recordOperation(c, auditedOperation{
		Scope:       auditScopeGlobal,
		ActorUserID: actorID,
		Action:      action,
		TargetType:  auditaction.TargetHelmRepository,
		TargetID:    targetID,
		TargetName:  targetName,
		Result:      result,
	})
}

func (handler *helmRepositoryHandler) respondRepositoryError(
	c *gin.Context,
	operation string,
	err error,
) bool {
	return handler.respondError(c, operation, err,
		errorMapping{
			helm.ErrInvalidInput,
			http.StatusBadRequest,
			"invalid_request",
			"invalid Helm chart repository request",
		},
		errorMapping{
			helm.ErrRepositoryNotFound,
			http.StatusNotFound,
			"helm_repository_not_found",
			"Helm chart repository not found",
		},
		errorMapping{
			helm.ErrRepositoryConflict,
			http.StatusConflict,
			"helm_repository_conflict",
			"a Helm chart repository with this name already exists",
		},
		errorMapping{
			helm.ErrRepositoryDisabled,
			http.StatusConflict,
			"helm_repository_disabled",
			"Helm chart repository is disabled",
		},
		errorMapping{
			helm.ErrChartNotFound,
			http.StatusNotFound,
			"chart_not_found",
			"chart or chart version was not found in this repository",
		},
		errorMapping{
			helm.ErrChartFileNotFound,
			http.StatusNotFound,
			"chart_file_not_found",
			"chart does not contain this file",
		},
		// 502 rather than 500: the failure is upstream of this Server, and an
		// operator reading it should go and look at the repository.
		errorMapping{
			helm.ErrRepositoryUnreachable,
			http.StatusBadGateway,
			"helm_repository_unreachable",
			"Helm chart repository could not be read",
		},
		errorMapping{
			helm.ErrChartTooLarge,
			http.StatusRequestEntityTooLarge,
			"chart_too_large",
			"chart archive exceeds the transferable size",
		},
		// The repository answered and the chart is there; what failed is the
		// platform's own rule about what it will accept from it. That is a 422
		// rather than a 502 — nothing upstream is broken, and an operator
		// should go and look at the signature, not at the server.
		errorMapping{
			helm.ErrChartUnsigned,
			http.StatusUnprocessableEntity,
			"chart_unsigned",
			"this repository requires signed charts and this version publishes no signature",
		},
		errorMapping{
			helm.ErrChartSignatureInvalid,
			http.StatusUnprocessableEntity,
			"chart_signature_invalid",
			"chart signature did not verify against this repository's keys",
		},
	)
}

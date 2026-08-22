package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/aimodel"
	"github.com/togettoyou/zke/pkg/server/audit"
	"github.com/togettoyou/zke/pkg/server/auditaction"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
)

const maxAIModelSettingsRequestBytes = 16 * 1024

type aiModelSettingsHandler struct {
	baseHandler
	service *aimodel.Service
	// probeTimeout bounds a connectivity test. It is not the settings timeout:
	// a probe waits on somebody else's inference service for as long as the
	// operator configured, which is far longer than any read of this section.
	probeTimeout time.Duration
}

// aiModelProbeMargin is what the request budget adds on top of the longest
// model call an operator can configure, so the probe's own deadline is always
// the one that fires. Without it the outer timeout would preempt the
// classification and report ZKE's budget as the endpoint's failure.
const aiModelProbeMargin = 15 * time.Second

// aiModelSettingsRequest is a whole-section save, with one exception.
//
// APIKey is a pointer because absent, empty and present are three different
// instructions: the stored key is never returned, so a form the operator did
// not touch has nothing to send back and must be able to say "leave it".
// Sending an empty string clears the key, which is what moving to an endpoint
// that takes no credential looks like.
type aiModelSettingsRequest struct {
	BaseURL             string  `json:"base_url"`
	Model               string  `json:"model"`
	APIProtocol         string  `json:"api_protocol"`
	APIKey              *string `json:"api_key"`
	ContextWindowTokens int     `json:"context_window_tokens"`
	MaxOutputTokens     int     `json:"max_output_tokens"`
	// Seconds rather than a duration string, so the Console binds it to a
	// numeric input without parsing anything.
	RequestTimeoutSeconds int64 `json:"request_timeout_seconds"`
	ExpectedRevision      int64 `json:"expected_revision"`
}

type aiModelEnabledRequest struct {
	Enabled          *bool `json:"enabled"`
	ExpectedRevision int64 `json:"expected_revision"`
}

func newAIModelSettingsHandler(
	logger *slog.Logger,
	service *aimodel.Service,
	auditService *audit.Service,
	timeout time.Duration,
) *aiModelSettingsHandler {
	return &aiModelSettingsHandler{
		baseHandler:  newBaseHandler(logger, auditService, timeout),
		service:      service,
		probeTimeout: aimodel.MaxRequestTimeout + aiModelProbeMargin,
	}
}

func (handler *aiModelSettingsHandler) get(c *gin.Context) {
	ctx, cancel := handler.operationContext(c)
	settings, err := handler.service.Get(ctx)
	cancel()
	if handler.respondAIModelError(c, "get AI model settings", err) {
		return
	}
	writeSuccess(c, http.StatusOK, aiModelSettingsResponse(settings))
}

func (handler *aiModelSettingsHandler) update(c *gin.Context) {
	identity, _ := httpmiddleware.Identity(c)
	var request aiModelSettingsRequest
	if err := decodeJSONRequest(c, &request, maxAIModelSettingsRequestBytes); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid AI model settings request")
		return
	}
	ctx, cancel := handler.operationContext(c)
	settings, err := handler.service.Update(ctx, aimodel.SettingsInput{
		BaseURL:             request.BaseURL,
		Model:               request.Model,
		APIProtocol:         aimodel.APIProtocol(request.APIProtocol),
		APIKey:              request.APIKey,
		ContextWindowTokens: request.ContextWindowTokens,
		MaxOutputTokens:     request.MaxOutputTokens,
		RequestTimeout:      aiModelRequestTimeout(request.RequestTimeoutSeconds),
		ExpectedRevision:    request.ExpectedRevision,
		ActorUserID:         identity.User.ID,
		Now:                 time.Now().UTC(),
	})
	cancel()
	if handler.respondAIModelError(c, "update AI model settings", err) {
		handler.auditAIModel(c, identity.User.ID, auditaction.AIModelSettingsUpdate, "failed")
		return
	}
	handler.auditAIModel(c, identity.User.ID, auditaction.AIModelSettingsUpdate, "succeeded")
	writeSuccess(c, http.StatusOK, aiModelSettingsResponse(settings))
}

func (handler *aiModelSettingsHandler) setEnabled(c *gin.Context) {
	identity, _ := httpmiddleware.Identity(c)
	var request aiModelEnabledRequest
	if err := decodeJSONRequest(c, &request, maxAIModelSettingsRequestBytes); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid AI model enabled request")
		return
	}
	if request.Enabled == nil {
		writeError(c, http.StatusBadRequest, "invalid_request", "AI model enabled state is required")
		return
	}
	ctx, cancel := handler.operationContext(c)
	settings, err := handler.service.SetEnabled(ctx, aimodel.EnabledInput{
		Enabled: *request.Enabled, ExpectedRevision: request.ExpectedRevision,
		ActorUserID: identity.User.ID, Now: time.Now().UTC(),
	})
	cancel()
	if handler.respondAIModelError(c, "set AI model enabled", err) {
		handler.auditAIModel(c, identity.User.ID, auditaction.AIModelEnabledUpdate, "failed")
		return
	}
	handler.auditAIModel(c, identity.User.ID, auditaction.AIModelEnabledUpdate, "succeeded")
	writeSuccess(c, http.StatusOK, aiModelSettingsResponse(settings))
}

// test reports whether the stored configuration can reach a model.
//
// A classified failure is a successful test — the operator asked a question and
// got an answer — so it is a 200 carrying the classification, not an error
// status. Only the platform having no endpoint at all is refused, because then
// there is nothing to ask about.
func (handler *aiModelSettingsHandler) test(c *gin.Context) {
	identity, _ := httpmiddleware.Identity(c)
	ctx, cancel := context.WithTimeout(c.Request.Context(), handler.probeTimeout)
	outcome, err := handler.service.Test(ctx)
	cancel()
	if handler.respondAIModelError(c, "test AI model endpoint", err) {
		handler.auditAIModel(c, identity.User.ID, auditaction.AIModelSettingsTest, "failed")
		return
	}
	result := "succeeded"
	if !outcome.Succeeded {
		result = "failed"
	}
	handler.auditAIModel(c, identity.User.ID, auditaction.AIModelSettingsTest, result)
	writeSuccess(c, http.StatusOK, gin.H{
		"succeeded": outcome.Succeeded,
		"failure":   string(outcome.Failure),
		"detail":    outcome.Detail,
		"status":    outcome.Status,
	})
}

// aiModelRequestTimeout converts requested seconds into a duration without
// letting an absurd number wrap around: multiplying an unbounded int64 by
// time.Second overflows into an arbitrary duration that could land inside the
// accepted range. Out-of-range input becomes zero, which the service refuses
// with the range it expects.
func aiModelRequestTimeout(seconds int64) time.Duration {
	if seconds <= 0 || seconds > int64(time.Hour/time.Second) {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

func (handler *aiModelSettingsHandler) respondAIModelError(c *gin.Context, operation string, err error) bool {
	return handler.respondError(c, operation, err,
		errorMapping{aimodel.ErrInvalidInput, http.StatusBadRequest, "invalid_ai_model_settings_input", "invalid AI model settings request"},
		errorMapping{aimodel.ErrConflict, http.StatusConflict, "revision_conflict", "settings were changed by another request"},
		errorMapping{aimodel.ErrNotConfigured, http.StatusConflict, "ai_model_not_configured", "AI model endpoint is not configured"},
	)
}

// auditAIModel records under the platform settings target: these routes are
// authorized as platform administration, and a denial on this route is already
// recorded against that target by the authorization middleware. Splitting the
// target would make the allowed and the denied records of the same request
// describe two different kinds of object.
func (handler *aiModelSettingsHandler) auditAIModel(c *gin.Context, userID, action, result string) {
	handler.recordOperation(c, auditedOperation{
		Scope:       auditScopeGlobal,
		ActorUserID: userID,
		Action:      action,
		TargetType:  auditaction.TargetPlatformSettings,
		Result:      result,
	})
}

func aiModelSettingsResponse(settings aimodel.Settings) gin.H {
	return gin.H{
		"enabled":                 settings.Enabled,
		"base_url":                settings.BaseURL,
		"model":                   settings.Model,
		"api_protocol":            settings.APIProtocol,
		"api_key_configured":      settings.APIKeyConfigured,
		"context_window_tokens":   settings.ContextWindowTokens,
		"max_output_tokens":       settings.MaxOutputTokens,
		"request_timeout_seconds": int64(settings.RequestTimeout / time.Second),
		"revision":                settings.Revision,
		"updated_at":              responseTime(settings.UpdatedAt),
	}
}

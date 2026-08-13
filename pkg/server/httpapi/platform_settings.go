package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/audit"
	"github.com/togettoyou/zke/pkg/server/auditaction"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
	"github.com/togettoyou/zke/pkg/server/platformsettings"
)

const maxPlatformSettingsRequestBytes = 64 * 1024

type platformSettingsHandler struct {
	baseHandler
	service *platformsettings.Service
}

type endpointProfileRequest struct {
	Name                         string `json:"name"`
	RegistrationURL              string `json:"registration_url"`
	QUICAddress                  string `json:"quic_address"`
	RegistrationCACertificatePEM string `json:"registration_ca_certificate_pem"`
	Enabled                      bool   `json:"enabled"`
	ExpectedRevision             int64  `json:"expected_revision"`
}

type platformSettingsRequest struct {
	AgentImage           string `json:"agent_image"`
	AgentNamespace       string `json:"agent_namespace"`
	AgentImagePullPolicy string `json:"agent_image_pull_policy"`
	ClusterTerminalImage string `json:"cluster_terminal_image"`
	ExpectedRevision     int64  `json:"expected_revision"`
}

func newPlatformSettingsHandler(logger *slog.Logger, service *platformsettings.Service, auditService *audit.Service, timeout time.Duration) *platformSettingsHandler {
	return &platformSettingsHandler{baseHandler: newBaseHandler(logger, auditService, timeout), service: service}
}

func (handler *platformSettingsHandler) get(c *gin.Context) {
	ctx, cancel := handler.operationContext(c)
	settings, profiles, err := handler.service.Get(ctx)
	cancel()
	if handler.respondPlatformError(c, "get platform settings", err) {
		return
	}
	writeSuccess(c, http.StatusOK, gin.H{"settings": settingsResponse(settings), "agent_endpoint_profiles": profilesResponse(profiles)})
}

func (handler *platformSettingsHandler) listReady(c *gin.Context) {
	ctx, cancel := handler.operationContext(c)
	defaultProfileID, profiles, err := handler.service.ListReadyProfiles(ctx)
	cancel()
	if handler.respondPlatformError(c, "list ready Agent endpoint profiles", err) {
		return
	}
	writeSuccess(c, http.StatusOK, gin.H{
		"default_endpoint_profile_id": defaultProfileID,
		"agent_endpoint_profiles":     profilesResponse(profiles),
	})
}

func (handler *platformSettingsHandler) createProfile(c *gin.Context) {
	identity, _ := httpmiddleware.Identity(c)
	var request endpointProfileRequest
	if err := decodeJSONRequest(c, &request, maxPlatformSettingsRequestBytes); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid Agent endpoint profile request")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.CreateProfile(ctx, platformsettings.ProfileInput{
		Name: request.Name, RegistrationURL: request.RegistrationURL, QUICAddress: request.QUICAddress,
		RegistrationCACertificatePEM: request.RegistrationCACertificatePEM,
		Enabled:                      request.Enabled, ActorUserID: identity.User.ID, Now: time.Now().UTC(),
	})
	cancel()
	if handler.respondProfileError(c, "create Agent endpoint profile", err) {
		handler.audit(c, identity.User.ID, auditaction.AgentEndpointProfileCreate, "", request.Name, "failed")
		return
	}
	handler.audit(c, identity.User.ID, auditaction.AgentEndpointProfileCreate, result.ID, result.Name, "succeeded")
	writeSuccess(c, http.StatusCreated, profileResponse(result))
}

func (handler *platformSettingsHandler) updateProfile(c *gin.Context) {
	identity, _ := httpmiddleware.Identity(c)
	var request endpointProfileRequest
	if err := decodeJSONRequest(c, &request, maxPlatformSettingsRequestBytes); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid Agent endpoint profile request")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.UpdateProfile(ctx, platformsettings.ProfileInput{
		ID: c.Param("profile_id"), Name: request.Name, RegistrationURL: request.RegistrationURL, QUICAddress: request.QUICAddress,
		RegistrationCACertificatePEM: request.RegistrationCACertificatePEM,
		Enabled:                      request.Enabled, ExpectedRevision: request.ExpectedRevision, ActorUserID: identity.User.ID, Now: time.Now().UTC(),
	})
	cancel()
	if handler.respondProfileError(c, "update Agent endpoint profile", err) {
		handler.audit(c, identity.User.ID, auditaction.AgentEndpointProfileUpdate, c.Param("profile_id"), request.Name, "failed")
		return
	}
	handler.audit(c, identity.User.ID, auditaction.AgentEndpointProfileUpdate, result.ID, result.Name, "succeeded")
	writeSuccess(c, http.StatusOK, profileResponse(result))
}

func (handler *platformSettingsHandler) deleteProfile(c *gin.Context) {
	identity, _ := httpmiddleware.Identity(c)
	ctx, cancel := handler.operationContext(c)
	err := handler.service.DeleteProfile(ctx, c.Param("profile_id"))
	cancel()
	if handler.respondProfileError(c, "delete Agent endpoint profile", err) {
		handler.audit(c, identity.User.ID, auditaction.AgentEndpointProfileDelete, c.Param("profile_id"), "", "failed")
		return
	}
	handler.audit(c, identity.User.ID, auditaction.AgentEndpointProfileDelete, c.Param("profile_id"), "", "succeeded")
	c.Status(http.StatusNoContent)
}

func (handler *platformSettingsHandler) updateSettings(c *gin.Context) {
	identity, _ := httpmiddleware.Identity(c)
	var request platformSettingsRequest
	if err := decodeJSONRequest(c, &request, maxPlatformSettingsRequestBytes); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid platform settings request")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.UpdateSettings(ctx, platformsettings.SettingsInput{
		AgentImage: request.AgentImage, AgentNamespace: request.AgentNamespace, AgentImagePullPolicy: request.AgentImagePullPolicy,
		ClusterTerminalImage: request.ClusterTerminalImage, ExpectedRevision: request.ExpectedRevision,
		ActorUserID: identity.User.ID, Now: time.Now().UTC(),
	})
	cancel()
	if handler.respondPlatformError(c, "update platform settings", err) {
		handler.auditSettings(c, identity.User.ID, "failed")
		return
	}
	handler.auditSettings(c, identity.User.ID, "succeeded")
	writeSuccess(c, http.StatusOK, settingsResponse(result))
}

func (handler *platformSettingsHandler) respondPlatformError(c *gin.Context, operation string, err error) bool {
	return handler.respondError(c, operation, err,
		errorMapping{platformsettings.ErrInvalidInput, http.StatusBadRequest, "invalid_request", "invalid platform settings request"},
		errorMapping{platformsettings.ErrNotFound, http.StatusNotFound, "not_found", "Agent endpoint profile not found"},
		errorMapping{platformsettings.ErrConflict, http.StatusConflict, "revision_conflict", "settings were changed by another request"},
		errorMapping{platformsettings.ErrInUse, http.StatusConflict, "profile_in_use", "Agent endpoint profile is the current default"},
		errorMapping{platformsettings.ErrEndpointUnready, http.StatusConflict, "endpoint_profile_unready", "Agent endpoint profile certificate is unavailable"},
	)
}

func (handler *platformSettingsHandler) respondProfileError(c *gin.Context, operation string, err error) bool {
	return handler.respondError(c, operation, err,
		errorMapping{platformsettings.ErrInvalidInput, http.StatusBadRequest, "invalid_endpoint_profile_input", "invalid Agent endpoint profile"},
		errorMapping{platformsettings.ErrNotFound, http.StatusNotFound, "not_found", "Agent endpoint profile not found"},
		errorMapping{platformsettings.ErrConflict, http.StatusConflict, "revision_conflict", "settings were changed by another request"},
		errorMapping{platformsettings.ErrInUse, http.StatusConflict, "profile_in_use", "Agent endpoint profile is the current default"},
		errorMapping{platformsettings.ErrEndpointUnready, http.StatusConflict, "endpoint_profile_unready", "Agent endpoint profile certificate is unavailable"},
	)
}

func (handler *platformSettingsHandler) audit(c *gin.Context, userID, action, targetID, targetName, result string) {
	handler.recordOperation(c, auditedOperation{Scope: auditScopeGlobal, ActorUserID: userID, Action: action, TargetType: auditaction.TargetAgentEndpointProfile, TargetID: targetID, TargetName: targetName, Result: result})
}

func (handler *platformSettingsHandler) auditSettings(c *gin.Context, userID, result string) {
	handler.recordOperation(c, auditedOperation{Scope: auditScopeGlobal, ActorUserID: userID, Action: auditaction.PlatformSettingsUpdate, TargetType: auditaction.TargetPlatformSettings, Result: result})
}

func profileResponse(profile platformsettings.Profile) gin.H {
	return gin.H{"id": profile.ID, "name": profile.Name, "registration_url": profile.RegistrationURL, "quic_address": profile.QUICAddress, "registration_ca_certificate_pem": profile.RegistrationCACertificatePEM, "enabled": profile.Enabled, "status": profile.Status, "revision": profile.Revision, "created_at": responseTime(profile.CreatedAt), "updated_at": responseTime(profile.UpdatedAt)}
}

func profilesResponse(profiles []platformsettings.Profile) []gin.H {
	result := make([]gin.H, 0, len(profiles))
	for _, profile := range profiles {
		result = append(result, profileResponse(profile))
	}
	return result
}

func settingsResponse(settings platformsettings.Settings) gin.H {
	return gin.H{"default_endpoint_profile_id": settings.DefaultEndpointProfileID, "agent_image": settings.AgentImage, "agent_namespace": settings.AgentNamespace, "agent_image_pull_policy": settings.AgentImagePullPolicy, "cluster_terminal_image": settings.ClusterTerminalImage, "revision": settings.Revision, "updated_at": responseTime(settings.UpdatedAt)}
}

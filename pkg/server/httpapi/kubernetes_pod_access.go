package httpapi

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/audit"
	"github.com/togettoyou/zke/pkg/server/auditaction"
	"github.com/togettoyou/zke/pkg/server/httpapi/middleware"
	apiresponse "github.com/togettoyou/zke/pkg/server/httpapi/response"
	"github.com/togettoyou/zke/pkg/server/podaccess"
	"github.com/togettoyou/zke/pkg/server/podportforward"
)

type podAccessService interface {
	Create(podaccess.CreateInput) (podaccess.Ticket, error)
}

type kubernetesPodAccessHandler struct {
	baseHandler
	service podAccessService
}

type podAccessCreateResponse struct {
	AccessURL         string    `json:"access_url"`
	ActivationExpires time.Time `json:"activation_expires_at"`
	SessionExpiresIn  int64     `json:"session_expires_in_seconds"`
}

type podAccessCreateRequest struct {
	PodUID                 string `json:"uid"`
	Port                   uint32 `json:"port"`
	SessionDurationSeconds *int64 `json:"session_duration_seconds"`
	ReplaceExisting        bool   `json:"replace_existing"`
	Confirm                bool   `json:"confirm"`
}

func newKubernetesPodAccessHandler(logger *slog.Logger, service podAccessService,
	auditService *audit.Service, operationTimeout time.Duration) *kubernetesPodAccessHandler {
	return &kubernetesPodAccessHandler{
		baseHandler: newBaseHandler(logger, auditService, operationTimeout),
		service:     service,
	}
}

func (handler *kubernetesPodAccessHandler) create(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, _ := middleware.Identity(c)
	request := podAccessCreateRequest{}
	target := podPortForwardTarget(c, request.PodUID, request.Port)
	if decodeJSONRequest(c, &request, maxPodPortForwardCreateBytes) != nil {
		handler.record(c, identity.User.ID, target, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid Pod access session request")
		return
	}
	sessionTTL := podAccessSessionTTL(request.SessionDurationSeconds)
	target = fmt.Sprintf("%s duration:%s replace:%t", podPortForwardTarget(c, request.PodUID, request.Port),
		sessionTTL, request.ReplaceExisting)
	sessionToken, tokenExists := middleware.SessionToken(c)
	if handler.service == nil || !tokenExists {
		handler.record(c, identity.User.ID, target, "failed")
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Pod access is unavailable")
		return
	}
	ticket, err := handler.service.Create(podaccess.CreateInput{
		UserID: identity.User.ID, AuthSessionID: identity.SessionID, AuthSessionToken: sessionToken,
		IdempotencyKey: c.GetHeader(idempotencyKeyHeaderName),
		ClusterID:      c.Param("cluster_id"), Namespace: c.Param("namespace_name"), PodName: c.Param("pod_name"),
		PodUID: request.PodUID, Port: request.Port, SessionTTL: sessionTTL,
		ReplaceExisting: request.ReplaceExisting, Confirm: request.Confirm,
		RequestID: middleware.RequestID(c), Now: time.Now().UTC(),
	})
	if err != nil {
		handler.record(c, identity.User.ID, target, "failed")
		if errors.Is(err, podaccess.ErrCapacity) {
			c.Header("Retry-After", "1")
		}
		handler.respondError(c, "create Kubernetes Pod access session", err,
			errorMapping{podaccess.ErrInvalidInput, http.StatusBadRequest, "invalid_request", "invalid Pod access session request"},
			errorMapping{podportforward.ErrConfirmationRequired, http.StatusBadRequest, "confirmation_required", "explicit confirmation is required"},
			errorMapping{podaccess.ErrIdempotencyConflict, http.StatusConflict, "idempotency_conflict", "idempotency key was already used for another Pod access request"},
			errorMapping{podaccess.ErrTargetReserved, http.StatusConflict, "pod_access_already_active", "Pod already has an active or pending access session"},
			errorMapping{podaccess.ErrCapacity, http.StatusTooManyRequests, "capacity_exhausted", "Pod access session capacity is exhausted"})
		return
	}
	handler.record(c, identity.User.ID, target, "succeeded")
	apiresponse.WriteSuccess(c, http.StatusCreated, podAccessCreateResponse{
		AccessURL: ticket.AccessURL, ActivationExpires: ticket.ExpiresAt,
		SessionExpiresIn: int64(ticket.SessionTTL.Seconds()),
	})
}

func podAccessSessionTTL(seconds *int64) time.Duration {
	// Requests created before selectable durations existed omitted this field.
	// Preserve their original 15-minute behavior while the current contract requires it.
	if seconds == nil {
		return 15 * time.Minute
	}
	switch *seconds {
	case int64((15 * time.Minute) / time.Second), int64((30 * time.Minute) / time.Second), int64(time.Hour / time.Second):
		return time.Duration(*seconds) * time.Second
	default:
		return 0
	}
}

func (handler *kubernetesPodAccessHandler) record(c *gin.Context, userID, target, result string) {
	handler.recordOperation(c, auditedOperation{
		Scope: auditScopeCluster, ActorUserID: userID,
		Action:     auditaction.KubernetesPodPortForwardSessionCreate,
		TargetType: auditaction.TargetKubernetesResource, TargetName: target, Result: result,
	})
}

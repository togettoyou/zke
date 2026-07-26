package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/agentstatus"
	"github.com/togettoyou/zke/pkg/server/auth"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
	"github.com/togettoyou/zke/pkg/server/rbac"
)

const (
	agentEventHeartbeatInterval = 15 * time.Second
	agentEventMaximumDuration   = 30 * time.Minute
	agentEventWriteTimeout      = 5 * time.Second
)

func (handler *agentStatusHandler) events(c *gin.Context) {
	if handler.service == nil ||
		handler.authService == nil ||
		handler.rbacService == nil {
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Agent events are unavailable")
		return
	}
	events, unsubscribe, err := handler.service.Subscribe()
	if errors.Is(err, agentstatus.ErrEventsUnavailable) {
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Agent events are unavailable")
		return
	}
	if err != nil {
		handler.logger.Error(
			"subscribe to Agent events",
			slog.String("request_id", httpmiddleware.RequestID(c)),
			slog.String("error", err.Error()),
		)
		writeError(c, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	defer unsubscribe()

	sessionCookie, err := c.Request.Cookie(httpmiddleware.SessionCookieName)
	if err != nil || sessionCookie.Value == "" {
		writeError(c, http.StatusUnauthorized, "unauthenticated", "authentication required")
		return
	}
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-store")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	if err := writeSSE(c.Writer, "", "ready", map[string]string{
		"request_id": httpmiddleware.RequestID(c),
	}); err != nil {
		return
	}

	heartbeat := time.NewTicker(agentEventHeartbeatInterval)
	defer heartbeat.Stop()
	maximumDuration := time.NewTimer(agentEventMaximumDuration)
	defer maximumDuration.Stop()

	for {
		select {
		case <-c.Request.Context().Done():
			return
		case <-maximumDuration.C:
			_ = writeSSE(c.Writer, "", "close", map[string]string{
				"reason": "maximum_duration",
			})
			return
		case <-heartbeat.C:
			if !handler.sessionStillActive(c, sessionCookie.Value) {
				return
			}
			if err := writeSSEComment(c.Writer, "heartbeat"); err != nil {
				return
			}
		case event := <-events:
			operationContext, cancel := context.WithTimeout(
				c.Request.Context(),
				handler.operationTimeout,
			)
			_, authorizeErr := handler.rbacService.AuthorizeCluster(
				operationContext,
				mustIdentity(c).User.ID,
				rbac.PermissionAgentRead,
				event.ClusterID,
			)
			if authorizeErr != nil {
				cancel()
				if errors.Is(authorizeErr, rbac.ErrDenied) {
					continue
				}
				handler.logger.Error(
					"authorize Agent status event",
					slog.String("request_id", httpmiddleware.RequestID(c)),
					slog.String("agent_id", event.AgentID),
					slog.String("error", authorizeErr.Error()),
				)
				return
			}
			status, statusErr := handler.service.GetCluster(
				operationContext,
				event.ClusterID,
				time.Now().UTC(),
			)
			cancel()
			if statusErr != nil {
				if !errors.Is(statusErr, agentstatus.ErrNotFound) {
					handler.logger.Error(
						"load Agent status event",
						slog.String("request_id", httpmiddleware.RequestID(c)),
						slog.String("agent_id", event.AgentID),
						slog.String("error", statusErr.Error()),
					)
				}
				continue
			}
			if err := writeSSE(
				c.Writer,
				event.ID,
				"agent.status",
				responseAgentStatus(status),
			); err != nil {
				return
			}
		}
	}
}

func (handler *agentStatusHandler) sessionStillActive(
	c *gin.Context,
	sessionToken string,
) bool {
	ctx, cancel := context.WithTimeout(
		c.Request.Context(),
		handler.operationTimeout,
	)
	defer cancel()
	identity, err := handler.authService.Authenticate(
		ctx,
		sessionToken,
		time.Now().UTC(),
	)
	if err != nil {
		return false
	}
	current := mustIdentity(c)
	return identity.SessionID == current.SessionID &&
		identity.User.ID == current.User.ID
}

func mustIdentity(c *gin.Context) auth.Identity {
	identity, _ := httpmiddleware.Identity(c)
	return identity
}

func writeSSE(
	writer http.ResponseWriter,
	id string,
	event string,
	data any,
) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("encode SSE event: %w", err)
	}
	controller := http.NewResponseController(writer)
	_ = controller.SetWriteDeadline(time.Now().Add(agentEventWriteTimeout))
	if id != "" {
		if _, err := fmt.Fprintf(writer, "id: %s\n", id); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(
		writer,
		"event: %s\ndata: %s\n\n",
		event,
		payload,
	); err != nil {
		return err
	}
	err = controller.Flush()
	_ = controller.SetWriteDeadline(time.Time{})
	return err
}

func writeSSEComment(writer http.ResponseWriter, value string) error {
	controller := http.NewResponseController(writer)
	_ = controller.SetWriteDeadline(time.Now().Add(agentEventWriteTimeout))
	if _, err := fmt.Fprintf(writer, ": %s\n\n", value); err != nil {
		return err
	}
	err := controller.Flush()
	_ = controller.SetWriteDeadline(time.Time{})
	return err
}

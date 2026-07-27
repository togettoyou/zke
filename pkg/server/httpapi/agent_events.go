package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/agentstatus"
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
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Cluster events are unavailable")
		return
	}
	sessionToken, exists := httpmiddleware.SessionToken(c)
	if !exists {
		writeError(c, http.StatusUnauthorized, "unauthenticated", "authentication required")
		return
	}

	// Resolve the caller's Cluster visibility once and filter the fan-out
	// against it in memory. Authorizing every event with a database round trip
	// would multiply queries by subscribers times event rate, and the fan-out
	// already carries the Tenant and Project each event belongs to.
	visibility, err := handler.clusterVisibility(c)
	if err != nil {
		handler.respondError(c, "resolve Cluster event visibility", err)
		return
	}
	// A caller who can observe no Cluster at all is refused before the stream
	// opens. Accepting the connection and filtering every event away would let
	// an unprivileged session hold a subscriber slot and a revalidation query
	// budget for the whole maximum stream duration.
	if !visibility.HasAny() {
		writeError(c, http.StatusForbidden, "forbidden", "permission denied")
		return
	}

	events, unsubscribe, err := handler.service.Subscribe()
	if errors.Is(err, agentstatus.ErrEventsUnavailable) {
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Cluster events are unavailable")
		return
	}
	if err != nil {
		handler.respondError(c, "subscribe to Cluster events", err)
		return
	}
	defer unsubscribe()

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
			// The heartbeat doubles as the revalidation point: a revoked
			// session or a withdrawn RoleBinding must end the stream rather
			// than keep feeding a caller who lost access.
			if !handler.sessionStillActive(c, sessionToken) {
				return
			}
			refreshed, err := handler.clusterVisibility(c)
			if err != nil {
				handler.logInternal(c, "refresh Cluster event visibility", err)
				return
			}
			visibility = refreshed
			if !visibility.HasAny() {
				return
			}
			if err := writeSSEComment(c.Writer, "heartbeat"); err != nil {
				return
			}
		case event := <-events:
			if !visibility.AllowsProject(event.TenantID, event.ProjectID) {
				continue
			}
			operationContext, cancel := handler.operationContext(c)
			status, statusErr := handler.service.GetCluster(
				operationContext,
				event.ClusterID,
				time.Now().UTC(),
			)
			cancel()
			if statusErr != nil {
				if !errors.Is(statusErr, agentstatus.ErrNotFound) {
					handler.logInternal(c, "load Cluster status event", statusErr)
				}
				continue
			}
			if err := writeSSE(
				c.Writer,
				event.ID,
				"cluster.status",
				responseAgentStatus(status),
			); err != nil {
				return
			}
		}
	}
}

// clusterVisibility resolves which Clusters the caller may observe.
func (handler *agentStatusHandler) clusterVisibility(
	c *gin.Context,
) (rbac.Visibility, error) {
	ctx, cancel := handler.operationContext(c)
	defer cancel()
	identity, _ := httpmiddleware.Identity(c)
	return handler.rbacService.ResolveVisibility(
		ctx,
		identity.User.ID,
		rbac.PermissionClusterRead,
	)
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
	current, _ := httpmiddleware.Identity(c)
	return identity.SessionID == current.SessionID &&
		identity.User.ID == current.User.ID
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

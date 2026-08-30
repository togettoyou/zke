package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/aimodel"
	"github.com/togettoyou/zke/pkg/server/airuntime"
	"github.com/togettoyou/zke/pkg/server/aisession"
	"github.com/togettoyou/zke/pkg/server/audit"
	"github.com/togettoyou/zke/pkg/server/auditaction"
	"github.com/togettoyou/zke/pkg/server/auth"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
	"github.com/togettoyou/zke/pkg/shared/validation"
)

const (
	maxAIRuntimeRequestBytes = 256 * 1024
	// aiStreamPollInterval is a backstop, not the delivery path. The runtime
	// wakes a watcher the moment it writes an entry; this catches the cases it
	// cannot reach, such as an entry written by another Server process.
	aiStreamPollInterval    = 5 * time.Second
	aiStreamRecheckInterval = 10 * time.Second
	// aiStreamWriteTimeout bounds one frame, not the stream. See
	// writeAIStreamEvent.
	aiStreamWriteTimeout = 5 * time.Second
	// aiStreamHeartbeatInterval keeps a quiet stream provably alive. A turn
	// spends most of its time waiting on a model, and without a frame in that
	// window neither the Server nor anything between it and the browser learns
	// that the connection is gone.
	aiStreamHeartbeatInterval = 15 * time.Second
	aiStreamMaximumDuration   = 30 * time.Minute
)

type aiRuntimeHandler struct {
	logger         *slog.Logger
	runtime        *airuntime.Runtime
	sessions       *aisession.Service
	auth           *auth.Service
	audit          *audit.Service
	operationLimit time.Duration
}

type aiCreateSessionRequest struct {
	TenantID     string `json:"tenant_id"`
	ProjectID    string `json:"project_id"`
	ClusterID    string `json:"cluster_id"`
	Title        string `json:"title"`
	ApprovalMode string `json:"approval_mode"`
}

type aiEvidenceRequest struct {
	Kind            string    `json:"kind"`
	Cluster         string    `json:"cluster"`
	Namespace       string    `json:"namespace"`
	GVK             string    `json:"gvk"`
	Name            string    `json:"name"`
	ResourceVersion string    `json:"resource_version"`
	Query           string    `json:"query"`
	Expression      string    `json:"expression"`
	Parameters      string    `json:"parameters"`
	Container       string    `json:"container"`
	From            time.Time `json:"from"`
	To              time.Time `json:"to"`
}

type aiStartTurnRequest struct {
	Text          string              `json:"text"`
	Evidence      []aiEvidenceRequest `json:"evidence"`
	AttachmentIDs []string            `json:"attachment_ids"`
}

type aiUpdateSessionRequest struct {
	Title        *string `json:"title"`
	Archived     *bool   `json:"archived"`
	ApprovalMode *string `json:"approval_mode"`
}

type aiApprovalRequest struct {
	CallID   string `json:"call_id"`
	Decision string `json:"decision"`
}

type aiAttachmentRequest struct {
	Name      string `json:"name"`
	MediaType string `json:"media_type"`
	Content   string `json:"content"`
}

func newAIRuntimeHandler(
	logger *slog.Logger,
	runtime *airuntime.Runtime,
	sessions *aisession.Service,
	authService *auth.Service,
	auditService *audit.Service,
	operationLimit time.Duration,
) *aiRuntimeHandler {
	return &aiRuntimeHandler{logger: logger, runtime: runtime, sessions: sessions, auth: authService,
		audit: auditService, operationLimit: operationLimit}
}

// tools reports whether AIOps is switched on, and the catalogue the runtime
// advertises to the model.
//
// A description of the runtime rather than of anything in a Cluster: it carries
// no cluster identity and reads nothing, so it needs authentication and not a
// scope. What it is for is the Console being able to say what AIOps can do
// before an operator has started a session — and whether to offer it at all,
// which is why the platform gate is answered here rather than only on the
// administrator-only settings route no operator can read.
func (handler *aiRuntimeHandler) tools(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), handler.operationLimit)
	enabled, err := handler.runtime.Enabled(ctx)
	cancel()
	if handler.respondError(c, "read AIOps availability", err) {
		return
	}
	specs := handler.runtime.ToolCatalogue()
	items := make([]gin.H, 0, len(specs))
	for _, spec := range specs {
		permissions := make([]string, 0, len(spec.Permissions))
		for _, permission := range spec.Permissions {
			permissions = append(permissions, string(permission))
		}
		conditionalPermissions := make([]string, 0, len(spec.ConditionalPermissions))
		for _, permission := range spec.ConditionalPermissions {
			conditionalPermissions = append(conditionalPermissions, string(permission))
		}
		items = append(items, gin.H{
			"name": spec.Name, "description": spec.Description, "permissions": permissions,
			"conditional_permissions": conditionalPermissions,
			"sensitive":               spec.Sensitive, "conditionally_sensitive": spec.SensitiveWhen != nil,
			"mutating": spec.Mutating,
		})
	}
	// Skills travel with the catalogue rather than on a route of their own:
	// they are the same fact about the runtime, the Console shows them in the
	// same place, and a second round trip would only make the two able to
	// disagree about which deployment they describe.
	skills := handler.runtime.SkillCatalogue()
	playbooks := make([]gin.H, 0, len(skills))
	for _, skill := range skills {
		playbooks = append(playbooks, gin.H{
			"id": skill.ID, "title": skill.Title, "summary": skill.Summary,
			"tools": append([]string{}, skill.Tools...),
		})
	}
	writeSuccess(c, http.StatusOK, gin.H{
		"enabled": enabled, "tools": items, "skills": playbooks,
	})
}

// decideApproval answers a call the runtime parked on a person.
//
// The decision is audited here rather than in the runtime because this is where
// the person is: the request carries their address and request id, and "who
// allowed this sensitive call or write" is the question the audit trail exists
// to answer.
func (handler *aiRuntimeHandler) decideApproval(c *gin.Context) {
	identity, _ := httpmiddleware.Identity(c)
	var request aiApprovalRequest
	if decodeJSONRequest(c, &request, maxAIRuntimeRequestBytes) != nil {
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid AIOps approval decision")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), handler.operationLimit)
	defer cancel()
	session, err := handler.runtime.Get(ctx, c.Param("session_id"), identity.User.ID, time.Now().UTC())
	if handler.respondError(c, "authorize AIOps approval", err) {
		return
	}
	err = handler.runtime.Decide(
		ctx, session.ID, identity.User.ID, request.CallID, request.Decision, time.Now().UTC(),
	)
	handler.recordApproval(c, identity.User.ID, session, request, err)
	if handler.respondError(c, "decide AIOps approval", err) {
		return
	}
	writeSuccess(c, http.StatusOK, nil)
}

func (handler *aiRuntimeHandler) recordApproval(
	c *gin.Context, userID string, session aisession.Session,
	request aiApprovalRequest, decideErr error,
) {
	if handler.audit == nil {
		return
	}
	result := "succeeded"
	if decideErr != nil {
		result = "failed"
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), handler.operationLimit)
	defer cancel()
	if err := handler.audit.RecordClusterEvent(ctx, audit.ClusterEventInput{
		ActorUserID: userID,
		ClusterID:   session.ClusterID,
		Action:      auditaction.AIApprovalDecide,
		TargetType:  auditaction.TargetAISession,
		TargetID:    session.ID,
		TargetName:  session.Title,
		Result:      result,
		RequestID:   httpmiddleware.RequestID(c),
		ActorIP:     c.ClientIP(),
		Detail: map[string]string{
			"decision": request.Decision,
			"call_id":  request.CallID,
		},
	}); err != nil {
		handler.logger.Error("record AIOps approval decision",
			slog.String("request_id", httpmiddleware.RequestID(c)), slog.String("error", err.Error()))
	}
}

func (handler *aiRuntimeHandler) createSession(c *gin.Context) {
	identity, _ := httpmiddleware.Identity(c)
	var request aiCreateSessionRequest
	if decodeJSONRequest(c, &request, maxAIRuntimeRequestBytes) != nil {
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid AIOps session request")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), handler.operationLimit)
	defer cancel()
	session, err := handler.runtime.Create(ctx, airuntime.CreateInput{
		UserID: identity.User.ID, TenantID: request.TenantID,
		ProjectID: request.ProjectID, ClusterID: request.ClusterID,
		Title: request.Title, ApprovalMode: aisession.ApprovalMode(request.ApprovalMode), Now: time.Now().UTC(),
	})
	if handler.respondError(c, "create AIOps session", err) {
		return
	}
	writeSuccess(c, http.StatusCreated, aiSessionResponse(session))
}

func (handler *aiRuntimeHandler) listSessions(c *gin.Context) {
	identity, _ := httpmiddleware.Identity(c)
	limit, _ := strconv.Atoi(c.Query("limit"))
	tenantID := c.Query("tenant_id")
	projectID := c.Query("project_id")
	clusterID := c.Query("cluster_id")
	ctx, cancel := context.WithTimeout(c.Request.Context(), handler.operationLimit)
	defer cancel()
	if err := handler.runtime.AuthorizeTarget(
		ctx, identity.User.ID, tenantID, projectID, clusterID,
	); handler.respondError(c, "authorize AIOps workspace", err) {
		return
	}
	archived := c.Query("archived") == "true"
	sessions, err := handler.sessions.Search(ctx, aisession.SearchInput{
		InitiatorUserID: identity.User.ID, TenantID: tenantID,
		ProjectID: projectID, ClusterID: clusterID,
		Query: c.Query("search"), Archived: archived,
		Limit: limit, Now: time.Now().UTC(),
	})
	if handler.respondError(c, "list AIOps sessions", err) {
		return
	}
	items := make([]gin.H, 0, len(sessions))
	for _, session := range sessions {
		if _, accessErr := handler.runtime.Get(ctx, session.ID, identity.User.ID, time.Now().UTC()); accessErr == nil {
			items = append(items, aiSessionResponse(session))
		}
	}
	writeSuccess(c, http.StatusOK, gin.H{"sessions": items})
}

func (handler *aiRuntimeHandler) updateSession(c *gin.Context) {
	identity, _ := httpmiddleware.Identity(c)
	var request aiUpdateSessionRequest
	if decodeJSONRequest(c, &request, maxAIRuntimeRequestBytes) != nil {
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid AIOps session update")
		return
	}
	fields := 0
	if request.Title != nil {
		fields++
	}
	if request.Archived != nil {
		fields++
	}
	if request.ApprovalMode != nil {
		fields++
	}
	if fields != 1 {
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid AIOps session update")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), handler.operationLimit)
	defer cancel()
	if _, err := handler.runtime.Get(ctx, c.Param("session_id"), identity.User.ID, time.Now().UTC()); handler.respondError(c, "authorize AIOps session update", err) {
		return
	}
	var session aisession.Session
	var err error
	if request.Title != nil {
		session, err = handler.sessions.Rename(ctx, c.Param("session_id"), identity.User.ID,
			*request.Title, time.Now().UTC())
	}
	if err == nil && request.Archived != nil {
		session, err = handler.sessions.SetArchived(ctx, c.Param("session_id"), identity.User.ID,
			*request.Archived, time.Now().UTC())
	}
	if err == nil && request.ApprovalMode != nil {
		session, err = handler.sessions.SetApprovalMode(ctx, c.Param("session_id"), identity.User.ID,
			aisession.ApprovalMode(*request.ApprovalMode), time.Now().UTC())
	}
	if handler.respondError(c, "update AIOps session", err) {
		return
	}
	writeSuccess(c, http.StatusOK, aiSessionResponse(session))
}

func (handler *aiRuntimeHandler) deleteSession(c *gin.Context) {
	identity, _ := httpmiddleware.Identity(c)
	ctx, cancel := context.WithTimeout(c.Request.Context(), handler.operationLimit)
	defer cancel()
	if _, err := handler.runtime.Get(
		ctx, c.Param("session_id"), identity.User.ID, time.Now().UTC(),
	); handler.respondError(c, "authorize AIOps session deletion", err) {
		return
	}
	if err := handler.sessions.Delete(
		ctx, c.Param("session_id"), identity.User.ID, time.Now().UTC(),
	); handler.respondError(c, "delete archived AIOps session", err) {
		return
	}
	c.Status(http.StatusNoContent)
}

func (handler *aiRuntimeHandler) getSession(c *gin.Context) {
	identity, _ := httpmiddleware.Identity(c)
	ctx, cancel := context.WithTimeout(c.Request.Context(), handler.operationLimit)
	defer cancel()
	session, err := handler.runtime.Get(ctx, c.Param("session_id"), identity.User.ID, time.Now().UTC())
	if handler.respondError(c, "get AIOps session", err) {
		return
	}
	writeSuccess(c, http.StatusOK, aiSessionResponse(session))
}

func (handler *aiRuntimeHandler) startTurn(c *gin.Context) {
	identity, _ := httpmiddleware.Identity(c)
	var request aiStartTurnRequest
	if decodeJSONRequest(c, &request, maxAIRuntimeRequestBytes) != nil {
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid AIOps turn request")
		return
	}
	evidence := make([]aisession.Evidence, 0, len(request.Evidence))
	for _, item := range request.Evidence {
		evidence = append(evidence, aisession.Evidence{
			Kind: aisession.EvidenceKind(item.Kind), Cluster: item.Cluster,
			Namespace: item.Namespace, GVK: item.GVK, Name: item.Name,
			ResourceVersion: item.ResourceVersion, Query: item.Query,
			Expression: item.Expression,
			Parameters: item.Parameters, Container: item.Container, From: item.From, To: item.To,
		})
	}
	attachments, err := handler.sessions.Attachments(c.Request.Context(), c.Param("session_id"), identity.User.ID, time.Now().UTC())
	if err != nil {
		handler.respondError(c, "list AIOps turn attachments", err)
		return
	}
	selectedAttachments := make([]aisession.Attachment, 0, len(request.AttachmentIDs))
	if len(request.AttachmentIDs) > 32 {
		writeError(c, http.StatusBadRequest, "invalid_ai_attachment", "invalid AIOps attachment selection")
		return
	}
	requestedAttachments := make(map[string]struct{}, len(request.AttachmentIDs))
	for _, id := range request.AttachmentIDs {
		if !validation.IsUUID(id) {
			writeError(c, http.StatusBadRequest, "invalid_ai_attachment", "invalid AIOps attachment selection")
			return
		}
		if _, duplicate := requestedAttachments[id]; duplicate {
			writeError(c, http.StatusBadRequest, "invalid_ai_attachment", "invalid AIOps attachment selection")
			return
		}
		requestedAttachments[id] = struct{}{}
	}
	for _, attachment := range attachments {
		if _, selected := requestedAttachments[attachment.ID]; selected {
			selectedAttachments = append(selectedAttachments, attachment)
			delete(requestedAttachments, attachment.ID)
		}
	}
	if len(requestedAttachments) > 0 {
		writeError(c, http.StatusBadRequest, "invalid_ai_attachment", "AIOps attachment was not found")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), handler.operationLimit)
	defer cancel()
	entry, err := handler.runtime.Start(ctx, airuntime.StartInput{
		SessionID: c.Param("session_id"), UserID: identity.User.ID,
		Text: request.Text, Evidence: evidence, Attachments: selectedAttachments, Now: time.Now().UTC(),
	})
	if handler.respondError(c, "start AIOps turn", err) {
		return
	}
	writeSuccess(c, http.StatusAccepted, aiEntryResponse(entry))
}

func (handler *aiRuntimeHandler) listAttachments(c *gin.Context) {
	identity, _ := httpmiddleware.Identity(c)
	ctx, cancel := context.WithTimeout(c.Request.Context(), handler.operationLimit)
	defer cancel()
	if _, err := handler.runtime.Get(ctx, c.Param("session_id"), identity.User.ID, time.Now().UTC()); handler.respondError(c, "authorize AIOps attachments", err) {
		return
	}
	attachments, err := handler.sessions.Attachments(ctx, c.Param("session_id"), identity.User.ID, time.Now().UTC())
	if handler.respondError(c, "list AIOps attachments", err) {
		return
	}
	items := make([]gin.H, 0, len(attachments))
	for _, attachment := range attachments {
		items = append(items, aiAttachmentResponse(attachment, false))
	}
	writeSuccess(c, http.StatusOK, gin.H{"attachments": items})
}

func (handler *aiRuntimeHandler) createAttachment(c *gin.Context) {
	identity, _ := httpmiddleware.Identity(c)
	var request aiAttachmentRequest
	if decodeJSONRequest(c, &request, maxAIRuntimeRequestBytes) != nil {
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid AIOps attachment")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), handler.operationLimit)
	defer cancel()
	if _, err := handler.runtime.Get(ctx, c.Param("session_id"), identity.User.ID, time.Now().UTC()); handler.respondError(c, "authorize AIOps attachment", err) {
		return
	}
	attachment, err := handler.sessions.AddAttachment(ctx, aisession.AttachmentInput{
		SessionID: c.Param("session_id"), InitiatorUserID: identity.User.ID,
		Name: request.Name, MediaType: request.MediaType, Content: request.Content, Now: time.Now().UTC(),
	})
	if handler.respondError(c, "create AIOps attachment", err) {
		return
	}
	writeSuccess(c, http.StatusCreated, aiAttachmentResponse(attachment, false))
}

func (handler *aiRuntimeHandler) deleteAttachment(c *gin.Context) {
	identity, _ := httpmiddleware.Identity(c)
	ctx, cancel := context.WithTimeout(c.Request.Context(), handler.operationLimit)
	defer cancel()
	if _, err := handler.runtime.Get(ctx, c.Param("session_id"), identity.User.ID, time.Now().UTC()); handler.respondError(c, "authorize AIOps attachment deletion", err) {
		return
	}
	err := handler.sessions.DeleteAttachment(ctx, c.Param("session_id"), c.Param("attachment_id"), identity.User.ID)
	if handler.respondError(c, "delete AIOps attachment", err) {
		return
	}
	writeSuccess(c, http.StatusOK, nil)
}

func (handler *aiRuntimeHandler) exportSession(c *gin.Context) {
	identity, _ := httpmiddleware.Identity(c)
	ctx, cancel := context.WithTimeout(c.Request.Context(), handler.operationLimit)
	defer cancel()
	session, err := handler.runtime.Get(ctx, c.Param("session_id"), identity.User.ID, time.Now().UTC())
	if handler.respondError(c, "get AIOps session export", err) {
		return
	}
	entries := make([]aisession.Entry, 0)
	var after int32
	for {
		page, pageErr := handler.runtime.Trajectory(ctx, aisession.TrajectoryQuery{
			SessionID: session.ID, InitiatorUserID: identity.User.ID, AfterSequence: after,
			Limit: 500, Now: time.Now().UTC(),
		})
		if pageErr != nil {
			err = pageErr
			break
		}
		entries = append(entries, page...)
		if len(page) < 500 {
			break
		}
		after = page[len(page)-1].Sequence
	}
	attachments, attachmentErr := handler.sessions.Attachments(ctx, session.ID, identity.User.ID, time.Now().UTC())
	if err == nil {
		err = attachmentErr
	}
	if handler.respondError(c, "export AIOps session", err) {
		return
	}
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=aiops-%s.json", session.ID))
	payload, marshalErr := json.Marshal(gin.H{"session": aiSessionResponse(session),
		"trajectory": aiEntryResponses(entries), "attachments": aiAttachmentResponses(attachments, true)})
	if marshalErr != nil {
		handler.respondError(c, "encode AIOps session export", marshalErr)
		return
	}
	c.Data(http.StatusOK, "application/vnd.zke.ai-session+json; charset=utf-8", payload)
}

func (handler *aiRuntimeHandler) cancelTurn(c *gin.Context) {
	identity, _ := httpmiddleware.Identity(c)
	ctx, cancel := context.WithTimeout(c.Request.Context(), handler.operationLimit)
	defer cancel()
	err := handler.runtime.Cancel(ctx, c.Param("session_id"), identity.User.ID, time.Now().UTC())
	if handler.respondError(c, "cancel AIOps turn", err) {
		return
	}
	writeSuccess(c, http.StatusOK, nil)
}

func (handler *aiRuntimeHandler) trajectory(c *gin.Context) {
	identity, _ := httpmiddleware.Identity(c)
	after := sequenceFromRequest(c)
	limit, _ := strconv.Atoi(c.Query("limit"))
	ctx, cancel := context.WithTimeout(c.Request.Context(), handler.operationLimit)
	defer cancel()
	entries, err := handler.runtime.Trajectory(ctx, aisession.TrajectoryQuery{
		SessionID: c.Param("session_id"), InitiatorUserID: identity.User.ID,
		AfterSequence: after, Limit: limit, Now: time.Now().UTC(),
	})
	if handler.respondError(c, "read AIOps trajectory", err) {
		return
	}
	writeSuccess(c, http.StatusOK, gin.H{"entries": aiEntryResponses(entries)})
}

// contextUsage reports how full this session's model context is.
//
// The composer shows it as a small ring, which is the only place an operator
// can see a long investigation approaching the point where it will be
// compacted. It is computed by the runtime rather than by the browser: the
// pressure that matters is the one the loop measures before every request, and
// a second implementation in the Console would drift from it the first time
// either side changed.
//
// A deployment whose endpoint is not configured has no window to report against
// and answers 409, which is what leaves the ring off the composer entirely
// rather than showing an occupancy of nothing.
func (handler *aiRuntimeHandler) contextUsage(c *gin.Context) {
	identity, _ := httpmiddleware.Identity(c)
	ctx, cancel := context.WithTimeout(c.Request.Context(), handler.operationLimit)
	defer cancel()
	usage, err := handler.runtime.Usage(
		ctx, c.Param("session_id"), identity.User.ID, time.Now().UTC(),
	)
	if handler.respondError(c, "read AIOps context usage", err) {
		return
	}
	writeSuccess(c, http.StatusOK, gin.H{
		"used_tokens":           usage.UsedTokens,
		"context_window_tokens": usage.ContextWindowTokens,
		"threshold_tokens":      usage.ThresholdTokens,
		"system_tokens":         usage.SystemTokens,
		"tools_tokens":          usage.ToolsTokens,
		"message_tokens":        usage.MessageTokens,
		"measured":              usage.Measured,
	})
}

// events streams one session to a watcher.
//
// Two sources, and the difference between them is the whole design. Durable
// entries are read from the trajectory by sequence, which is why Last-Event-ID
// resumes exactly and why a Server restart does not lose a turn. Live deltas
// come from the runtime in-process broker, carry no sequence and are never
// replayed: they are the answer being typed, and the entry that follows is what
// the record, the export and any later review are built from.
func (handler *aiRuntimeHandler) events(c *gin.Context) {
	identity, _ := httpmiddleware.Identity(c)
	sessionToken, ok := httpmiddleware.SessionToken(c)
	if !ok {
		writeError(c, http.StatusUnauthorized, "unauthenticated", "authentication required")
		return
	}
	if _, ok := c.Writer.(http.Flusher); !ok {
		writeError(c, http.StatusInternalServerError, "stream_unavailable", "streaming is unavailable")
		return
	}
	// Subscribing before the catch-up read is what closes the gap: an entry
	// written between the two arrives as a wake rather than being missed until
	// the next tick.
	signals, release := handler.runtime.Subscribe(c.Param("session_id"))
	defer release()
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache, no-transform")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	// The Server write timeout bounds one ordinary response. This one is a
	// live stream that stays deliberately quiet while a model thinks, and
	// leaving the connection deadline in place kills it mid-turn — silently,
	// because net/http buffers the frames the handler goes on writing, so the
	// browser keeps a connection it believes is live and the operator waits on
	// a spinner for an answer that is already in the trail. Every write re-arms
	// its own bounded deadline instead, which is what still ends the stream
	// when a client stops reading.
	_ = http.NewResponseController(c.Writer).SetWriteDeadline(time.Time{})
	if writeAIStreamEvent(c.Writer, "", "ready", gin.H{}) != nil {
		return
	}
	after := sequenceFromRequest(c)
	poll := time.NewTicker(aiStreamPollInterval)
	recheck := time.NewTicker(aiStreamRecheckInterval)
	heartbeat := time.NewTicker(aiStreamHeartbeatInterval)
	maximum := time.NewTimer(aiStreamMaximumDuration)
	defer poll.Stop()
	defer recheck.Stop()
	defer heartbeat.Stop()
	defer maximum.Stop()
	var sessionState string
	for {
		if !handler.writeSessionState(c, identity.User.ID, &sessionState) {
			return
		}
		if !handler.writeAvailableEntries(c, identity.User.ID, &after) {
			return
		}
		// Deltas are forwarded without re-reading the trail. A fast model
		// produces hundreds of them a second, and re-authorizing each one would
		// mean hundreds of trajectory queries a second for text the database
		// does not hold. Authorization is re-established by the read at the top
		// of this loop, which every wake and every tick returns to, and the
		// browser session is reauthenticated on its own interval.
		awake := false
		for !awake {
			select {
			case <-c.Request.Context().Done():
				return
			case signal := <-signals:
				if signal.Type == airuntime.StreamEntries {
					awake = true
					continue
				}
				if !writeStreamDelta(c, signal) {
					return
				}
			case <-poll.C:
				awake = true
			case <-heartbeat.C:
				if writeAIStreamComment(c.Writer, "keep-alive") != nil {
					return
				}
			case <-recheck.C:
				ctx, cancel := context.WithTimeout(c.Request.Context(), handler.operationLimit)
				fresh, err := handler.auth.Authenticate(ctx, sessionToken, time.Now().UTC())
				cancel()
				if err != nil || fresh.User.ID != identity.User.ID {
					_ = writeAIStreamEvent(c.Writer, "", "close", gin.H{"reason": "unauthenticated"})
					return
				}
			case <-maximum.C:
				_ = writeAIStreamEvent(c.Writer, "", "close", gin.H{"reason": "duration"})
				return
			}
		}
	}
}

// writeAIStreamEvent writes one SSE frame and flushes it to the client.
//
// The deadline is re-armed per frame rather than left on the connection: a
// stream that is allowed to stay open for half an hour must still not be held
// by a client that has stopped reading. The flush is what reports that, so its
// error decides whether the stream continues.
func writeAIStreamEvent(writer http.ResponseWriter, id, event string, data any) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	controller := http.NewResponseController(writer)
	_ = controller.SetWriteDeadline(time.Now().Add(aiStreamWriteTimeout))
	defer func() { _ = controller.SetWriteDeadline(time.Time{}) }()
	if id != "" {
		if _, err := fmt.Fprintf(writer, "id: %s\n", id); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(writer, "event: %s\ndata: %s\n\n", event, payload); err != nil {
		return err
	}
	return controller.Flush()
}

// writeAIStreamComment writes an SSE comment: a frame with no event, ignored by
// the browser, whose only job is to fail when the client is no longer there.
func writeAIStreamComment(writer http.ResponseWriter, value string) error {
	controller := http.NewResponseController(writer)
	_ = controller.SetWriteDeadline(time.Now().Add(aiStreamWriteTimeout))
	defer func() { _ = controller.SetWriteDeadline(time.Time{}) }()
	if _, err := fmt.Fprintf(writer, ": %s\n\n", value); err != nil {
		return err
	}
	return controller.Flush()
}

// writeStreamDelta emits live output. Deliberately without an SSE id: giving it
// one would make a reconnect resume from a delta rather than from a durable
// sequence, and deltas are not replayable.
func writeStreamDelta(c *gin.Context, signal airuntime.StreamEvent) bool {
	return writeAIStreamEvent(c.Writer, "", "delta", gin.H{
		"kind": signal.Type, "turn": signal.Turn, "step": signal.Step, "text": signal.Text,
	}) == nil
}

// writeSessionState publishes the session record when something about it
// changed.
//
// The trail says what the turn did; the session says what the conversation is
// called and whether it is still running, and neither of those is an entry. A
// Console that only followed entries kept a spinner running after the turn
// ended and kept showing "新对话 16:04" after the first turn had named the
// conversation — both facts had changed, and nothing on the stream said so.
func (handler *aiRuntimeHandler) writeSessionState(
	c *gin.Context, userID string, last *string,
) bool {
	ctx, cancel := context.WithTimeout(c.Request.Context(), handler.operationLimit)
	session, err := handler.runtime.Get(ctx, c.Param("session_id"), userID, time.Now().UTC())
	cancel()
	if err != nil {
		_ = writeAIStreamEvent(c.Writer, "", "close", gin.H{"reason": "forbidden"})
		return false
	}
	state := fmt.Sprintf("%s|%s|%d|%s|%s|%v", session.Title, session.Status, session.CurrentTurn,
		session.LastTurnStatus, session.LastTurnFailure, session.ArchivedAt)
	if state == *last {
		return true
	}
	*last = state
	return writeAIStreamEvent(c.Writer, "", "session", aiSessionResponse(session)) == nil
}

func (handler *aiRuntimeHandler) writeAvailableEntries(
	c *gin.Context, userID string, after *int32,
) bool {
	ctx, cancel := context.WithTimeout(c.Request.Context(), handler.operationLimit)
	entries, err := handler.runtime.Trajectory(ctx, aisession.TrajectoryQuery{
		SessionID: c.Param("session_id"), InitiatorUserID: userID,
		AfterSequence: *after, Limit: 500, Now: time.Now().UTC(),
	})
	cancel()
	if err != nil {
		_ = writeAIStreamEvent(c.Writer, "", "close", gin.H{"reason": "forbidden"})
		return false
	}
	for _, entry := range entries {
		if writeAIStreamEvent(c.Writer, strconv.FormatInt(int64(entry.Sequence), 10),
			"trajectory", aiEntryResponse(entry)) != nil {
			return false
		}
		*after = entry.Sequence
	}
	return true
}

func sequenceFromRequest(c *gin.Context) int32 {
	value := strings.TrimSpace(c.GetHeader("Last-Event-ID"))
	if value == "" {
		value = c.Query("after_sequence")
	}
	parsed, err := strconv.ParseInt(value, 10, 32)
	if err != nil || parsed < 0 {
		return 0
	}
	return int32(parsed)
}

func (handler *aiRuntimeHandler) respondError(c *gin.Context, operation string, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, airuntime.ErrInvalidInput), errors.Is(err, aisession.ErrInvalidInput):
		writeError(c, http.StatusBadRequest, "invalid_ai_input", "invalid AIOps request")
	case errors.Is(err, airuntime.ErrForbidden):
		writeError(c, http.StatusForbidden, "ai_run_forbidden", "AIOps access denied")
	case errors.Is(err, aisession.ErrNotFound):
		writeError(c, http.StatusNotFound, "ai_session_not_found", "AIOps session not found")
	case errors.Is(err, airuntime.ErrAlreadyRunning), errors.Is(err, aisession.ErrBusy):
		writeError(c, http.StatusConflict, "ai_session_busy", "AIOps session already has a turn running")
	case errors.Is(err, airuntime.ErrNoRunningTurn), errors.Is(err, aisession.ErrIdle):
		writeError(c, http.StatusConflict, "ai_session_idle", "AIOps session has no turn running")
	case errors.Is(err, airuntime.ErrNoPendingApproval):
		writeError(c, http.StatusConflict, "ai_approval_not_pending",
			"AIOps approval request is no longer pending")
	case errors.Is(err, aisession.ErrNotArchived):
		writeError(c, http.StatusConflict, "ai_session_not_archived", "archive the AIOps session before deleting it")
	case errors.Is(err, aimodel.ErrDisabled), errors.Is(err, aimodel.ErrNotConfigured), errors.Is(err, airuntime.ErrModelNotReady):
		writeError(c, http.StatusServiceUnavailable, "ai_model_not_ready", "AIOps model is not ready")
	default:
		handler.logger.Error(operation,
			slog.String("request_id", httpmiddleware.RequestID(c)), slog.String("error", err.Error()))
		writeError(c, http.StatusInternalServerError, "internal_error", "internal server error")
	}
	return true
}

func aiSessionResponse(session aisession.Session) gin.H {
	result := gin.H{
		"id": session.ID, "tenant_id": session.TenantID,
		"project_id": session.ProjectID, "cluster_id": session.ClusterID,
		"title": session.Title, "status": session.Status, "approval_mode": session.ApprovalMode,
		"current_turn": session.CurrentTurn, "last_turn_status": session.LastTurnStatus,
		"last_turn_failure": session.LastTurnFailure, "created_at": session.CreatedAt,
		"last_activity_at": session.LastActivityAt,
	}
	if session.ArchivedAt != nil {
		result["archived_at"] = *session.ArchivedAt
	}
	return result
}

func aiAttachmentResponse(attachment aisession.Attachment, includeContent bool) gin.H {
	result := gin.H{"id": attachment.ID, "session_id": attachment.SessionID, "name": attachment.Name,
		"media_type": attachment.MediaType, "size_bytes": len(attachment.Content), "created_at": attachment.CreatedAt}
	if includeContent {
		result["content"] = attachment.Content
	}
	return result
}

func aiAttachmentResponses(attachments []aisession.Attachment, includeContent bool) []gin.H {
	result := make([]gin.H, 0, len(attachments))
	for _, attachment := range attachments {
		result = append(result, aiAttachmentResponse(attachment, includeContent))
	}
	return result
}

func aiEntryResponses(entries []aisession.Entry) []gin.H {
	result := make([]gin.H, 0, len(entries))
	for _, entry := range entries {
		result = append(result, aiEntryResponse(entry))
	}
	return result
}

func aiEntryResponse(entry aisession.Entry) gin.H {
	return gin.H{
		"sequence": entry.Sequence, "turn": entry.Turn, "kind": entry.Kind,
		"occurred_at": entry.OccurredAt, "duration_ms": entry.Duration.Milliseconds(),
		"truncated": entry.Truncated, "content": entry.Content,
	}
}

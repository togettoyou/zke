package aisession

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/shared/identifier"
	"github.com/togettoyou/zke/pkg/shared/validation"
)

var (
	ErrInvalidInput = errors.New("invalid AIOps session input")
	// ErrNotFound covers a session that does not exist, has aged out, or
	// belongs to somebody else — the reads cannot tell those apart on purpose.
	ErrNotFound = errors.New("AIOps session not found")
	// ErrBusy reports a question asked while the previous one is still being
	// answered.
	ErrBusy = errors.New("AIOps session already has a turn running")
	// ErrIdle reports a write with no turn running.
	ErrIdle = errors.New("AIOps session has no turn running")
	// ErrNotArchived is returned when deletion is attempted before the explicit
	// archive step.
	ErrNotArchived   = errors.New("AIOps session must be archived before deletion")
	ErrQuotaExceeded = errors.New("AIOps daily quota exceeded")
)

// defaultRetention is how long a session stays readable after its last use.
//
// Thirty days is a protective value, not a measured one: a trail holds cluster
// content and its lifetime should be far shorter than the audit record that
// says the session ran, but there is no baseline yet for how large these rows
// actually get. See the Phase 4 design's open questions.
const defaultRetention = 30 * 24 * time.Hour

const (
	defaultPageSize = 50
	maxPageSize     = 500
)

type Store interface {
	CreateAISession(context.Context, store.CreateAISessionParams) (store.AISession, error)
	SetAISessionApprovalMode(
		context.Context, string, string, string, []byte, time.Time,
	) (store.AISession, error)
	StartAITurn(context.Context, store.StartAITurnParams) (store.AISessionEvent, error)
	AppendAISessionEvent(
		context.Context, store.AppendAISessionEventParams,
	) (store.AISessionEvent, error)
	FinishAITurn(context.Context, store.FinishAITurnParams) (store.AISession, error)
	GetAISessionForInitiator(context.Context, string, string, time.Time) (store.AISession, error)
	ListAISessionsForInitiator(context.Context, string, string, string, string, time.Time, int) ([]store.AISession, error)
	ListAISessionEventsForInitiator(
		context.Context, string, string, int32, time.Time, int,
	) ([]store.AISessionEvent, error)
	InterruptAITurns(context.Context, store.InterruptAITurnsParams) (int64, error)
}

type AppStore interface {
	SearchAISessionsForInitiator(context.Context, store.SearchAISessionsParams) ([]store.AISession, error)
	UpdateAISessionTitle(context.Context, string, string, string, time.Time, time.Time) (store.AISession, error)
	SetAISessionArchived(context.Context, string, string, bool, time.Time, time.Time) (store.AISession, error)
	DeleteAISessionForInitiator(context.Context, string, string, time.Time) error
	CreateAISessionAttachment(context.Context, store.CreateAISessionAttachmentParams) (store.AISessionAttachment, error)
	ListAISessionAttachmentsForInitiator(context.Context, string, string, time.Time) ([]store.AISessionAttachment, error)
	DeleteAISessionAttachmentForInitiator(context.Context, string, string, string) error
}

type QualityStore interface {
	GetAIUsage(context.Context, string, string, string, time.Time, time.Time) (store.AIUsage, error)
	UpsertAITurnFeedback(context.Context, store.UpsertAITurnFeedbackParams) (store.AITurnFeedback, error)
	GetAITurnFeedback(context.Context, string, int32, string) (store.AITurnFeedback, error)
	EvaluateAI(context.Context, store.AIEvaluationQuery) (store.AIEvaluation, error)
}

type Config struct {
	// Retention is how long a session stays readable after its last activity.
	// Zero takes the package default.
	Retention time.Duration
	// DailyTurnLimit and DailyTokenLimit apply to one user in one Project and
	// reset at UTC midnight. Zero preserves unlimited existing deployments.
	DailyTurnLimit  int64
	DailyTokenLimit int64
}

type Service struct {
	store  Store
	config Config
}

func NewService(sessionStore Store, config Config) *Service {
	if config.Retention <= 0 {
		config.Retention = defaultRetention
	}
	return &Service{store: sessionStore, config: config}
}

type CreateInput struct {
	InitiatorUserID string
	// TenantID and ProjectID follow the Console's current desktop scope.
	// ClusterID is the workspace selected for this session.
	TenantID  string
	ProjectID string
	ClusterID string
	// Title is what the session is called in the list. TitleFrom turns a
	// question into one.
	Title string
	// ApprovalMode defaults to ask. It controls future model-driven operations,
	// but never changes the session's fixed Cluster or grants a permission.
	ApprovalMode ApprovalMode
	Now          time.Time
}

// StartTurnInput opens a turn with the question that opened it. The question
// and the turn are one act: a turn with nothing asked in it is not a turn.
type StartTurnInput struct {
	SessionID       string
	InitiatorUserID string
	TenantID        string
	ProjectID       string
	Content         Content
	Now             time.Time
}

type AppendInput struct {
	SessionID  string
	Kind       Kind
	Content    Content
	OccurredAt time.Time
	Duration   time.Duration
}

type FinishTurnInput struct {
	SessionID string
	Status    TurnStatus
	// Failure classifies a failed turn and must be empty for any other
	// outcome: a turn that succeeded has no reason to carry one.
	Failure string
	Now     time.Time
}

type TrajectoryQuery struct {
	SessionID       string
	InitiatorUserID string
	// AfterSequence is what the caller already has. A client that saw up to N
	// asks for what came after N and gets neither a duplicate nor a gap, which
	// is how a reconnect resumes rather than replays.
	AfterSequence int32
	Limit         int
	Now           time.Time
}

type SearchInput struct {
	InitiatorUserID string
	TenantID        string
	ProjectID       string
	ClusterID       string
	Query           string
	Archived        bool
	Limit           int
	Now             time.Time
}

type Attachment struct {
	ID        string
	SessionID string
	Name      string
	MediaType string
	Content   string
	CreatedAt time.Time
}

type AttachmentInput struct {
	SessionID       string
	InitiatorUserID string
	Name            string
	MediaType       string
	Content         string
	Now             time.Time
}

type Quota struct {
	PeriodStart  time.Time `json:"period_start"`
	PeriodEnd    time.Time `json:"period_end"`
	TurnsUsed    int64     `json:"turns_used"`
	TurnsLimit   int64     `json:"turns_limit"`
	InputTokens  int64     `json:"input_tokens"`
	OutputTokens int64     `json:"output_tokens"`
	TokensUsed   int64     `json:"tokens_used"`
	TokensLimit  int64     `json:"tokens_limit"`
	Exhausted    bool      `json:"exhausted"`
}

type Feedback struct {
	SessionID string    `json:"session_id"`
	Turn      int32     `json:"turn"`
	Rating    string    `json:"rating"`
	Outcome   string    `json:"outcome"`
	Reasons   []string  `json:"reasons"`
	Comment   string    `json:"comment"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type FeedbackInput struct {
	SessionID       string
	Turn            int32
	InitiatorUserID string
	Rating          string
	Outcome         string
	Reasons         []string
	Comment         string
	Now             time.Time
}

type EvaluationInput struct {
	InitiatorUserID string
	TenantID        string
	ProjectID       string
	ClusterID       string
	From            time.Time
	To              time.Time
}

type Evaluation struct {
	From          time.Time        `json:"from"`
	To            time.Time        `json:"to"`
	Turns         int64            `json:"turns"`
	Succeeded     int64            `json:"succeeded"`
	Failed        int64            `json:"failed"`
	Canceled      int64            `json:"canceled"`
	Rated         int64            `json:"rated"`
	Helpful       int64            `json:"helpful"`
	Resolved      int64            `json:"resolved"`
	ToolCalls     int64            `json:"tool_calls"`
	DurationMS    int64            `json:"duration_ms"`
	FailureCounts map[string]int64 `json:"failure_counts"`
	ReasonCounts  map[string]int64 `json:"reason_counts"`
}

// Create opens a session with no turns in it yet.
func (service *Service) Create(ctx context.Context, input CreateInput) (Session, error) {
	if !validation.IsUUID(input.InitiatorUserID) || !validation.IsUUID(input.TenantID) ||
		!validation.IsUUID(input.ProjectID) ||
		!validation.IsUUID(input.ClusterID) || input.Now.IsZero() {
		return Session{}, ErrInvalidInput
	}
	title := TitleFrom(input.Title)
	if title == "" {
		return Session{}, ErrInvalidInput
	}
	mode := input.ApprovalMode
	if mode == "" {
		mode = ApprovalAsk
	}
	if !mode.Valid() {
		return Session{}, ErrInvalidInput
	}
	id, err := identifier.NewUUID()
	if err != nil {
		return Session{}, err
	}
	created, err := service.store.CreateAISession(ctx, store.CreateAISessionParams{
		ID:              id,
		InitiatorUserID: input.InitiatorUserID,
		TenantID:        input.TenantID,
		ProjectID:       input.ProjectID,
		ClusterID:       input.ClusterID,
		Title:           title,
		ApprovalMode:    string(mode),
		Now:             input.Now,
		RetentionCutoff: service.cutoff(input.Now),
	})
	if err != nil {
		return Session{}, err
	}
	return sessionFromStore(created), nil
}

// SetApprovalMode changes how far a session may go without asking.
//
// Allowed at any time, including while a turn is running — the control belongs
// in the composer, and an operator who sees AIOps about to do something is
// exactly the person who wants to change the mode right then. Switching
// mid-turn appends a `system` entry, so the trail says which mode each part of
// the turn ran under rather than only which mode is set now.
func (service *Service) SetApprovalMode(
	ctx context.Context,
	sessionID string,
	initiatorUserID string,
	mode ApprovalMode,
	now time.Time,
) (Session, error) {
	if !validation.IsUUID(sessionID) || !validation.IsUUID(initiatorUserID) ||
		!mode.Valid() || now.IsZero() {
		return Session{}, ErrInvalidInput
	}
	// Only the classification, no prose: the Console renders the mode, and a
	// display string written into the trail would be one nobody can translate
	// later.
	note, err := json.Marshal(Content{Mode: mode})
	if err != nil {
		return Session{}, fmt.Errorf("encode AIOps approval mode change: %w", err)
	}
	updated, err := service.store.SetAISessionApprovalMode(
		ctx, sessionID, initiatorUserID, string(mode), note, now,
	)
	if err != nil {
		return Session{}, translateStoreError(err)
	}
	return sessionFromStore(updated), nil
}

// StartTurn opens the next turn and records the question. Everything the turn
// then does is appended to it until FinishTurn closes it.
func (service *Service) StartTurn(ctx context.Context, input StartTurnInput) (Entry, error) {
	if !validation.IsUUID(input.SessionID) || !validation.IsUUID(input.InitiatorUserID) ||
		!validation.IsUUID(input.TenantID) || !validation.IsUUID(input.ProjectID) || input.Now.IsZero() {
		return Entry{}, ErrInvalidInput
	}
	if err := validateContent(KindInput, input.Content); err != nil {
		return Entry{}, err
	}
	content := input.Content
	truncated := content.normalize(KindInput)
	encoded, err := json.Marshal(content)
	if err != nil {
		return Entry{}, fmt.Errorf("encode AIOps question: %w", err)
	}
	opened, err := service.store.StartAITurn(ctx, store.StartAITurnParams{
		SessionID: input.SessionID, InitiatorUserID: input.InitiatorUserID,
		TenantID: input.TenantID, ProjectID: input.ProjectID,
		Content: encoded, Truncated: truncated, OccurredAt: input.Now,
		TurnLimit: service.config.DailyTurnLimit, TokenLimit: service.config.DailyTokenLimit,
	})
	if err != nil {
		return Entry{}, translateStoreError(err)
	}
	return entryOf(opened, content), nil
}

// Append writes one entry into the running turn and returns it as stored,
// sequence and truncation included — the caller pushes what was recorded rather
// than what it meant to record, so a watcher and a later reader see the same
// thing.
func (service *Service) Append(ctx context.Context, input AppendInput) (Entry, error) {
	if !validation.IsUUID(input.SessionID) || !input.Kind.valid() ||
		input.OccurredAt.IsZero() || input.Duration < 0 {
		return Entry{}, ErrInvalidInput
	}
	// The question is written by StartTurn, which is also what opens the turn.
	// Allowing a second path to write one would allow a turn with two of them.
	if input.Kind == KindInput {
		return Entry{}, ErrInvalidInput
	}
	if err := validateContent(input.Kind, input.Content); err != nil {
		return Entry{}, err
	}
	content := input.Content
	truncated := content.normalize(input.Kind)
	encoded, err := json.Marshal(content)
	if err != nil {
		return Entry{}, fmt.Errorf("encode AIOps trail entry: %w", err)
	}
	appended, err := service.store.AppendAISessionEvent(ctx, store.AppendAISessionEventParams{
		SessionID:  input.SessionID,
		Kind:       string(input.Kind),
		Content:    encoded,
		Truncated:  truncated,
		OccurredAt: input.OccurredAt,
		Duration:   input.Duration,
	})
	if err != nil {
		return Entry{}, translateStoreError(err)
	}
	return entryOf(appended, content), nil
}

// FinishTurn closes the running turn. Appending the entry that explains the
// ending — an error, a last conclusion — happens before this call, because
// after it the turn is closed.
func (service *Service) FinishTurn(ctx context.Context, input FinishTurnInput) (Session, error) {
	if !validation.IsUUID(input.SessionID) || input.Now.IsZero() {
		return Session{}, ErrInvalidInput
	}
	switch input.Status {
	case TurnFailed:
		if !validFailure(input.Failure) {
			return Session{}, ErrInvalidInput
		}
	case TurnSucceeded, TurnCanceled:
		if input.Failure != "" {
			return Session{}, ErrInvalidInput
		}
	default:
		return Session{}, ErrInvalidInput
	}
	finished, err := service.store.FinishAITurn(ctx, store.FinishAITurnParams{
		SessionID: input.SessionID,
		Status:    string(input.Status),
		Failure:   input.Failure,
		Now:       input.Now,
	})
	if err != nil {
		return Session{}, translateStoreError(err)
	}
	return sessionFromStore(finished), nil
}

// Get reads one session for its owner. This storage-level ownership predicate
// is not the complete history authorization rule: the future HTTP layer must
// recheck current access before returning cluster-derived entry bodies.
func (service *Service) Get(
	ctx context.Context,
	sessionID string,
	initiatorUserID string,
	now time.Time,
) (Session, error) {
	if !validation.IsUUID(sessionID) || !validation.IsUUID(initiatorUserID) || now.IsZero() {
		return Session{}, ErrInvalidInput
	}
	found, err := service.store.GetAISessionForInitiator(
		ctx, sessionID, initiatorUserID, service.cutoff(now),
	)
	if err != nil {
		return Session{}, translateStoreError(err)
	}
	return sessionFromStore(found), nil
}

// List reads one person's own sessions, most recently used first.
func (service *Service) List(
	ctx context.Context,
	initiatorUserID string,
	tenantID string,
	projectID string,
	clusterID string,
	now time.Time,
	limit int,
) ([]Session, error) {
	if !validation.IsUUID(initiatorUserID) || !validation.IsUUID(tenantID) ||
		!validation.IsUUID(projectID) || !validation.IsUUID(clusterID) || now.IsZero() {
		return nil, ErrInvalidInput
	}
	found, err := service.store.ListAISessionsForInitiator(
		ctx, initiatorUserID, tenantID, projectID, clusterID, service.cutoff(now), pageSize(limit),
	)
	if err != nil {
		return nil, err
	}
	sessions := make([]Session, 0, len(found))
	for _, item := range found {
		sessions = append(sessions, sessionFromStore(item))
	}
	return sessions, nil
}

func (service *Service) Search(ctx context.Context, input SearchInput) ([]Session, error) {
	if !validation.IsUUID(input.InitiatorUserID) || !validation.IsUUID(input.TenantID) ||
		!validation.IsUUID(input.ProjectID) || !validation.IsUUID(input.ClusterID) ||
		input.Now.IsZero() || len(input.Query) > 200 {
		return nil, ErrInvalidInput
	}
	appStore, ok := service.store.(AppStore)
	if !ok {
		return nil, errors.New("AIOps app store is unavailable")
	}
	found, err := appStore.SearchAISessionsForInitiator(ctx, store.SearchAISessionsParams{
		InitiatorUserID: input.InitiatorUserID, TenantID: input.TenantID,
		ProjectID: input.ProjectID, ClusterID: input.ClusterID,
		RetentionCutoff: service.cutoff(input.Now),
		Query:           strings.TrimSpace(input.Query), Archived: input.Archived, Limit: pageSize(input.Limit),
	})
	if err != nil {
		return nil, err
	}
	result := make([]Session, 0, len(found))
	for _, item := range found {
		result = append(result, sessionFromStore(item))
	}
	return result, nil
}

func (service *Service) Rename(
	ctx context.Context, sessionID, userID, title string, now time.Time,
) (Session, error) {
	if !validation.IsUUID(sessionID) || !validation.IsUUID(userID) || now.IsZero() {
		return Session{}, ErrInvalidInput
	}
	title = TitleFrom(title)
	if title == "" {
		return Session{}, ErrInvalidInput
	}
	appStore, ok := service.store.(AppStore)
	if !ok {
		return Session{}, errors.New("AIOps app store is unavailable")
	}
	updated, err := appStore.UpdateAISessionTitle(ctx, sessionID, userID, title, now, service.cutoff(now))
	if err != nil {
		return Session{}, translateStoreError(err)
	}
	return sessionFromStore(updated), nil
}

func (service *Service) SetArchived(
	ctx context.Context, sessionID, userID string, archived bool, now time.Time,
) (Session, error) {
	if !validation.IsUUID(sessionID) || !validation.IsUUID(userID) || now.IsZero() {
		return Session{}, ErrInvalidInput
	}
	appStore, ok := service.store.(AppStore)
	if !ok {
		return Session{}, errors.New("AIOps app store is unavailable")
	}
	updated, err := appStore.SetAISessionArchived(ctx, sessionID, userID, archived, now, service.cutoff(now))
	if err != nil {
		return Session{}, translateStoreError(err)
	}
	return sessionFromStore(updated), nil
}

func (service *Service) Delete(
	ctx context.Context, sessionID, userID string, now time.Time,
) error {
	if !validation.IsUUID(sessionID) || !validation.IsUUID(userID) || now.IsZero() {
		return ErrInvalidInput
	}
	appStore, ok := service.store.(AppStore)
	if !ok {
		return errors.New("AIOps app store is unavailable")
	}
	return translateStoreError(appStore.DeleteAISessionForInitiator(
		ctx, sessionID, userID, service.cutoff(now),
	))
}

func (service *Service) AddAttachment(ctx context.Context, input AttachmentInput) (Attachment, error) {
	name := strings.TrimSpace(input.Name)
	if !validation.IsUUID(input.SessionID) || !validation.IsUUID(input.InitiatorUserID) ||
		input.Now.IsZero() || name == "" || len(name) > 200 || len(input.Content) == 0 ||
		len(input.Content) > 256*1024 || !validAttachmentMediaType(input.MediaType) {
		return Attachment{}, ErrInvalidInput
	}
	id, err := identifier.NewUUID()
	if err != nil {
		return Attachment{}, err
	}
	appStore, ok := service.store.(AppStore)
	if !ok {
		return Attachment{}, errors.New("AIOps app store is unavailable")
	}
	created, err := appStore.CreateAISessionAttachment(ctx, store.CreateAISessionAttachmentParams{
		ID: id, SessionID: input.SessionID, InitiatorUserID: input.InitiatorUserID,
		Name: name, MediaType: input.MediaType, Content: input.Content, CreatedAt: input.Now,
		RetentionCutoff: service.cutoff(input.Now),
	})
	if err != nil {
		return Attachment{}, translateStoreError(err)
	}
	return attachmentFromStore(created), nil
}

func (service *Service) Attachments(
	ctx context.Context, sessionID, userID string, now time.Time,
) ([]Attachment, error) {
	if !validation.IsUUID(sessionID) || !validation.IsUUID(userID) || now.IsZero() {
		return nil, ErrInvalidInput
	}
	appStore, ok := service.store.(AppStore)
	if !ok {
		return nil, errors.New("AIOps app store is unavailable")
	}
	stored, err := appStore.ListAISessionAttachmentsForInitiator(ctx, sessionID, userID, service.cutoff(now))
	if err != nil {
		return nil, err
	}
	result := make([]Attachment, 0, len(stored))
	for _, item := range stored {
		result = append(result, attachmentFromStore(item))
	}
	return result, nil
}

func (service *Service) DeleteAttachment(
	ctx context.Context, sessionID, attachmentID, userID string,
) error {
	if !validation.IsUUID(sessionID) || !validation.IsUUID(attachmentID) || !validation.IsUUID(userID) {
		return ErrInvalidInput
	}
	appStore, ok := service.store.(AppStore)
	if !ok {
		return errors.New("AIOps app store is unavailable")
	}
	return translateStoreError(appStore.DeleteAISessionAttachmentForInitiator(
		ctx, sessionID, attachmentID, userID,
	))
}

func (service *Service) Quota(
	ctx context.Context,
	initiatorUserID, tenantID, projectID string,
	now time.Time,
) (Quota, error) {
	if !validation.IsUUID(initiatorUserID) || !validation.IsUUID(tenantID) ||
		!validation.IsUUID(projectID) || now.IsZero() {
		return Quota{}, ErrInvalidInput
	}
	qualityStore, ok := service.store.(QualityStore)
	if !ok {
		return Quota{}, errors.New("AIOps quality store is unavailable")
	}
	periodStart := now.UTC().Truncate(24 * time.Hour)
	usage, err := qualityStore.GetAIUsage(ctx, initiatorUserID, tenantID, projectID,
		periodStart, periodStart.Add(24*time.Hour))
	if err != nil {
		return Quota{}, err
	}
	tokensUsed := usage.InputTokens + usage.OutputTokens
	return Quota{
		PeriodStart: usage.PeriodStart, PeriodEnd: usage.PeriodEnd,
		TurnsUsed: usage.Turns, TurnsLimit: service.config.DailyTurnLimit,
		InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens,
		TokensUsed: tokensUsed, TokensLimit: service.config.DailyTokenLimit,
		Exhausted: (service.config.DailyTurnLimit > 0 && usage.Turns >= service.config.DailyTurnLimit) ||
			(service.config.DailyTokenLimit > 0 && tokensUsed >= service.config.DailyTokenLimit),
	}, nil
}

func (service *Service) SaveFeedback(ctx context.Context, input FeedbackInput) (Feedback, error) {
	comment := strings.TrimSpace(input.Comment)
	if !validation.IsUUID(input.SessionID) || !validation.IsUUID(input.InitiatorUserID) ||
		input.Turn < 1 || input.Now.IsZero() || len(comment) > 1000 ||
		!validFeedbackRating(input.Rating) || !validFeedbackOutcome(input.Outcome) ||
		len(input.Reasons) > 6 {
		return Feedback{}, ErrInvalidInput
	}
	seen := make(map[string]struct{}, len(input.Reasons))
	for _, reason := range input.Reasons {
		if !validFeedbackReason(reason) {
			return Feedback{}, ErrInvalidInput
		}
		if _, duplicate := seen[reason]; duplicate {
			return Feedback{}, ErrInvalidInput
		}
		seen[reason] = struct{}{}
	}
	qualityStore, ok := service.store.(QualityStore)
	if !ok {
		return Feedback{}, errors.New("AIOps quality store is unavailable")
	}
	stored, err := qualityStore.UpsertAITurnFeedback(ctx, store.UpsertAITurnFeedbackParams{
		SessionID: input.SessionID, Turn: input.Turn, InitiatorUserID: input.InitiatorUserID,
		Rating: input.Rating, Outcome: input.Outcome, Reasons: input.Reasons,
		Comment: comment, Now: input.Now,
	})
	if err != nil {
		return Feedback{}, translateStoreError(err)
	}
	return feedbackFromStore(stored), nil
}

func (service *Service) Feedback(
	ctx context.Context, sessionID string, turn int32, initiatorUserID string,
) (Feedback, error) {
	if !validation.IsUUID(sessionID) || !validation.IsUUID(initiatorUserID) || turn < 1 {
		return Feedback{}, ErrInvalidInput
	}
	qualityStore, ok := service.store.(QualityStore)
	if !ok {
		return Feedback{}, errors.New("AIOps quality store is unavailable")
	}
	stored, err := qualityStore.GetAITurnFeedback(ctx, sessionID, turn, initiatorUserID)
	if err != nil {
		return Feedback{}, translateStoreError(err)
	}
	return feedbackFromStore(stored), nil
}

func (service *Service) Evaluate(ctx context.Context, input EvaluationInput) (Evaluation, error) {
	if !validation.IsUUID(input.InitiatorUserID) || !validation.IsUUID(input.TenantID) ||
		!validation.IsUUID(input.ProjectID) || !validation.IsUUID(input.ClusterID) ||
		input.From.IsZero() || input.To.IsZero() || !input.From.Before(input.To) ||
		input.To.Sub(input.From) > 90*24*time.Hour {
		return Evaluation{}, ErrInvalidInput
	}
	qualityStore, ok := service.store.(QualityStore)
	if !ok {
		return Evaluation{}, errors.New("AIOps quality store is unavailable")
	}
	stored, err := qualityStore.EvaluateAI(ctx, store.AIEvaluationQuery{
		InitiatorUserID: input.InitiatorUserID, TenantID: input.TenantID,
		ProjectID: input.ProjectID, ClusterID: input.ClusterID, From: input.From, To: input.To,
	})
	if err != nil {
		return Evaluation{}, err
	}
	return Evaluation{
		From: input.From, To: input.To, Turns: stored.Turns, Succeeded: stored.Succeeded,
		Failed: stored.Failed, Canceled: stored.Canceled, Rated: stored.Rated,
		Helpful: stored.Helpful, Resolved: stored.Resolved, ToolCalls: stored.ToolCalls,
		DurationMS: stored.Duration.Milliseconds(), FailureCounts: stored.FailureCounts,
		ReasonCounts: stored.ReasonCounts,
	}, nil
}

// Trajectory reads a session's entries in order.
//
// A session the caller may not read produces an empty page rather than an
// error, because the store's predicate covers the session and the entries in
// one query: there is no state in which the entries are readable and the
// session is not.
func (service *Service) Trajectory(
	ctx context.Context,
	query TrajectoryQuery,
) ([]Entry, error) {
	if !validation.IsUUID(query.SessionID) || !validation.IsUUID(query.InitiatorUserID) ||
		query.AfterSequence < 0 || query.Now.IsZero() {
		return nil, ErrInvalidInput
	}
	found, err := service.store.ListAISessionEventsForInitiator(
		ctx,
		query.SessionID,
		query.InitiatorUserID,
		query.AfterSequence,
		service.cutoff(query.Now),
		pageSize(query.Limit),
	)
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, len(found))
	for _, item := range found {
		var content Content
		if err := json.Unmarshal(item.Content, &content); err != nil {
			return nil, fmt.Errorf("decode AIOps trail entry %d: %w", item.Sequence, err)
		}
		entries = append(entries, entryOf(item, content))
	}
	return entries, nil
}

// RecoverInterrupted ends every turn left running by a Server that is no longer
// here, writing one error entry into each trail, and reports how many it ended.
//
// A turn is driven by a goroutine in one process. A session still marked
// working after that process is gone describes something that is not happening,
// and leaving it would show an operator a turn that never advances and never
// ends. Called once at startup, before anything can open a new turn.
func (service *Service) RecoverInterrupted(ctx context.Context, now time.Time) (int64, error) {
	if now.IsZero() {
		return 0, ErrInvalidInput
	}
	// Only the classification, no prose: the Console translates it, and a
	// display string written into the database would be one nobody can
	// translate later.
	content, err := json.Marshal(Content{Failure: FailureInterrupted})
	if err != nil {
		return 0, fmt.Errorf("encode AIOps interruption: %w", err)
	}
	return service.store.InterruptAITurns(ctx, store.InterruptAITurnsParams{
		Failure: FailureInterrupted,
		Content: content,
		Now:     now,
	})
}

func (service *Service) cutoff(now time.Time) time.Time {
	return now.Add(-service.config.Retention)
}

// validateContent refuses an entry that would be unreadable as its kind. It
// checks what the kind means rather than every field: an entry may carry more
// than its kind requires, and rendering ignores what it does not use.
func validateContent(kind Kind, content Content) error {
	switch kind {
	case KindSystem:
		// A system entry carries the instruction text, or nothing but the mode
		// when it is the note left by a mid-turn switch.
		if strings.TrimSpace(content.Text) == "" && !content.Mode.Valid() {
			return ErrInvalidInput
		}
	case KindInput, KindContext, KindReasoning, KindConclusion:
		if strings.TrimSpace(content.Text) == "" {
			return ErrInvalidInput
		}
	case KindModel:
		// A model step says something, calls something, or both. One that did
		// neither is not a step worth a row.
		if strings.TrimSpace(content.Text) == "" && len(content.Tools) == 0 {
			return ErrInvalidInput
		}
	case KindToolCall, KindToolResult, KindApprovalRequest:
		if strings.TrimSpace(content.Tool) == "" {
			return ErrInvalidInput
		}
	case KindApprovalDecision:
		if strings.TrimSpace(content.Tool) == "" || !validDecision(content.Decision) {
			return ErrInvalidInput
		}
	case KindCompaction:
		if !content.Compaction.valid() {
			return ErrInvalidInput
		}
	case KindError:
		if !validFailure(content.Failure) {
			return ErrInvalidInput
		}
	}
	return nil
}

func translateStoreError(err error) error {
	switch {
	case errors.Is(err, store.ErrAISessionNotFound):
		return ErrNotFound
	case errors.Is(err, store.ErrAISessionBusy):
		return ErrBusy
	case errors.Is(err, store.ErrAISessionIdle):
		return ErrIdle
	case errors.Is(err, store.ErrAISessionNotArchived):
		return ErrNotArchived
	case errors.Is(err, store.ErrAIQuotaExceeded):
		return ErrQuotaExceeded
	default:
		return err
	}
}

func pageSize(limit int) int {
	if limit <= 0 {
		return defaultPageSize
	}
	if limit > maxPageSize {
		return maxPageSize
	}
	return limit
}

func entryOf(stored store.AISessionEvent, content Content) Entry {
	return Entry{
		Sequence:   stored.Sequence,
		Turn:       stored.Turn,
		Kind:       Kind(stored.Kind),
		OccurredAt: stored.OccurredAt,
		Duration:   stored.Duration,
		Truncated:  stored.Truncated,
		Content:    content,
	}
}

func sessionFromStore(item store.AISession) Session {
	return Session{
		ID:              item.ID,
		InitiatorUserID: item.InitiatorUserID,
		TenantID:        item.TenantID,
		ProjectID:       item.ProjectID,
		ClusterID:       item.ClusterID,
		Title:           item.Title,
		Status:          Status(item.Status),
		ApprovalMode:    ApprovalMode(item.ApprovalMode),
		CurrentTurn:     item.CurrentTurn,
		LastTurnStatus:  TurnStatus(item.LastTurnStatus),
		LastTurnFailure: item.LastTurnFailure,
		CreatedAt:       item.CreatedAt,
		LastActivityAt:  item.LastActivityAt,
		ArchivedAt:      item.ArchivedAt,
	}
}

func validAttachmentMediaType(value string) bool {
	switch value {
	case "text/plain", "text/markdown", "application/json", "application/yaml":
		return true
	default:
		return false
	}
}

func attachmentFromStore(item store.AISessionAttachment) Attachment {
	return Attachment{ID: item.ID, SessionID: item.SessionID, Name: item.Name,
		MediaType: item.MediaType, Content: item.Content, CreatedAt: item.CreatedAt}
}

func feedbackFromStore(item store.AITurnFeedback) Feedback {
	return Feedback{SessionID: item.SessionID, Turn: item.Turn, Rating: item.Rating,
		Outcome: item.Outcome, Reasons: item.Reasons, Comment: item.Comment,
		CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt}
}

func validFeedbackRating(value string) bool {
	return value == "helpful" || value == "not_helpful"
}

func validFeedbackOutcome(value string) bool {
	return value == "resolved" || value == "unresolved" || value == "unsure"
}

func validFeedbackReason(value string) bool {
	switch value {
	case "inaccurate", "insufficient_evidence", "incomplete", "unsafe", "hard_to_follow", "other":
		return true
	default:
		return false
	}
}

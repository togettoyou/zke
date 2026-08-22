// Package airuntime drives AIOps turns after the initiating HTTP request has
// returned.
//
// The shape is an agent loop, not a pipeline: the model decides which tools to
// call and in what order, and the loop keeps going until it stops asking for
// anything. What the loop does not delegate is authority. It owns no
// Kubernetes credential, every tool call is authorized again against the
// operator current RBAC before it runs, cluster output is data and never
// instruction, and the durable trail is written before anything is pushed to a
// watcher.
package airuntime

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/togettoyou/zke/pkg/server/aimodel"
	"github.com/togettoyou/zke/pkg/server/aisession"
	"github.com/togettoyou/zke/pkg/server/audit"
	"github.com/togettoyou/zke/pkg/server/auditaction"
	"github.com/togettoyou/zke/pkg/server/rbac"
	"github.com/togettoyou/zke/pkg/shared/validation"
)

var (
	ErrInvalidInput   = errors.New("invalid AIOps runtime input")
	ErrForbidden      = errors.New("AIOps runtime access denied")
	ErrUserInactive   = errors.New("AIOps user is inactive")
	ErrAlreadyRunning = errors.New("AIOps turn already running")
	ErrNoRunningTurn  = errors.New("AIOps turn is not running")
	ErrModelNotReady  = errors.New("AIOps model is not ready")
	ErrContextBudget  = errors.New("AIOps context budget exceeded")
)

const (
	trajectoryPageSize = 500
	// maxTitleOutputTokens is deliberately more than a title needs: a reasoning
	// model spends most of an output budget thinking, and a budget sized to the
	// title itself would cut every answer off before it started.
	maxTitleOutputTokens = 1024
	// maxTitleRunes is what fits a conversation row without truncation.
	maxTitleRunes = 24
)

type UserStore interface {
	IsAIUserActive(context.Context, string) (bool, error)
}

type SessionService interface {
	Create(context.Context, aisession.CreateInput) (aisession.Session, error)
	Get(context.Context, string, string, time.Time) (aisession.Session, error)
	List(context.Context, string, string, string, string, time.Time, int) ([]aisession.Session, error)
	StartTurn(context.Context, aisession.StartTurnInput) (aisession.Entry, error)
	Append(context.Context, aisession.AppendInput) (aisession.Entry, error)
	FinishTurn(context.Context, aisession.FinishTurnInput) (aisession.Session, error)
	Trajectory(context.Context, aisession.TrajectoryQuery) ([]aisession.Entry, error)
	Rename(context.Context, string, string, string, time.Time) (aisession.Session, error)
}

type ModelService interface {
	Get(context.Context) (aimodel.Settings, error)
	Complete(context.Context, aimodel.CompletionInput) (aimodel.Completion, aimodel.Budget, error)
}

type Authorizer interface {
	AuthorizeCluster(context.Context, string, rbac.Permission, string) (rbac.ResolvedScope, error)
	ResolveClusterScope(context.Context, string) (rbac.ResolvedScope, error)
}

// Auditor records what AIOps did to a Cluster.
//
// A separate record from the trajectory, and not a duplicate of it: the trail
// is a short-lived account of one conversation, kept for weeks and readable
// only by the person who started it, while the audit trail is the deployment
// long-term account of who touched which Cluster. Every AIOps call performed on
// an operator behalf has to appear in the second one too; write records also
// name their mutating flag and resolved resource target.
type Auditor interface {
	RecordClusterEvent(context.Context, audit.ClusterEventInput) error
}

type Runtime struct {
	base       context.Context
	sessions   SessionService
	model      ModelService
	authorizer Authorizer
	users      UserStore
	tools      ToolSet
	audit      Auditor

	turnTimeout          time.Duration
	maxSteps             int
	maxToolCalls         int
	maxParallelToolCalls int
	repeatedCallLimit    int
	approvalTimeout      time.Duration
	titleTimeout         time.Duration
	retry                RetryConfig
	compaction           CompactionConfig

	stream    *broker
	approvals *pendingApprovals

	mu      sync.Mutex
	jobs    map[string]context.CancelFunc
	waiting sync.WaitGroup
}

func New(
	base context.Context,
	sessions SessionService,
	model ModelService,
	authorizer Authorizer,
	users UserStore,
	config Config,
) *Runtime {
	config = config.normalized()
	return &Runtime{
		base: base, sessions: sessions, model: model, authorizer: authorizer, users: users,
		tools: config.Tools, audit: config.Audit,
		turnTimeout:          config.TurnTimeout,
		maxSteps:             config.MaxSteps,
		maxToolCalls:         config.MaxToolCalls,
		maxParallelToolCalls: config.MaxParallelToolCalls,
		repeatedCallLimit:    config.RepeatedCallLimit,
		approvalTimeout:      config.ApprovalTimeout,
		titleTimeout:         config.TitleTimeout,
		retry:                config.ModelRetry,
		compaction:           config.Compaction,
		stream:               newBroker(),
		approvals:            newPendingApprovals(),
		jobs:                 make(map[string]context.CancelFunc),
	}
}

type CreateInput struct {
	UserID       string
	TenantID     string
	ProjectID    string
	ClusterID    string
	Title        string
	ApprovalMode aisession.ApprovalMode
	Now          time.Time
}

type StartInput struct {
	SessionID   string
	UserID      string
	Text        string
	Evidence    []aisession.Evidence
	Attachments []aisession.Attachment
	Now         time.Time
}

// ToolCatalogue reports what the model may call, for the Console to show before
// a turn is started. It carries no arguments and no cluster identity: it is a
// description of the runtime, not an authorization decision.
func (runtime *Runtime) ToolCatalogue() []ToolSpec {
	if runtime.tools == nil {
		return nil
	}
	return runtime.tools.Specs()
}

// Enabled reports whether the platform has AIOps turned on and pointed at an
// endpoint.
//
// The Console asks so it can leave the application off the desktop entirely
// rather than offering a workspace whose every turn would be refused. It is a
// fact about the deployment, not about the caller, so it carries no scope — and
// it deliberately says nothing about the endpoint itself, which stays inside
// the platform settings the administrator reads.
func (runtime *Runtime) Enabled(ctx context.Context) (bool, error) {
	settings, err := runtime.model.Get(ctx)
	if err != nil {
		return false, err
	}
	return settings.Enabled && settings.BaseURL != "" && settings.Model != "", nil
}

func (runtime *Runtime) Create(ctx context.Context, input CreateInput) (aisession.Session, error) {
	if err := runtime.AuthorizeTarget(ctx, input.UserID, input.TenantID, input.ProjectID, input.ClusterID); err != nil {
		return aisession.Session{}, err
	}
	return runtime.sessions.Create(ctx, aisession.CreateInput{
		InitiatorUserID: input.UserID, TenantID: input.TenantID,
		ProjectID: input.ProjectID, ClusterID: input.ClusterID, Title: input.Title,
		ApprovalMode: input.ApprovalMode, Now: input.Now,
	})
}

func (runtime *Runtime) Get(
	ctx context.Context, sessionID, userID string, now time.Time,
) (aisession.Session, error) {
	session, err := runtime.sessions.Get(ctx, sessionID, userID, now)
	if err != nil {
		return aisession.Session{}, err
	}
	if err := runtime.AuthorizeTarget(ctx, userID, session.TenantID, session.ProjectID, session.ClusterID); err != nil {
		return aisession.Session{}, err
	}
	return session, nil
}

func (runtime *Runtime) List(
	ctx context.Context, userID, tenantID, projectID, clusterID string, now time.Time, limit int,
) ([]aisession.Session, error) {
	if err := runtime.AuthorizeTarget(ctx, userID, tenantID, projectID, clusterID); err != nil {
		return nil, err
	}
	sessions, err := runtime.sessions.List(ctx, userID, tenantID, projectID, clusterID, now, limit)
	if err != nil {
		return nil, err
	}
	visible := make([]aisession.Session, 0, len(sessions))
	for _, session := range sessions {
		if runtime.AuthorizeTarget(ctx, userID, session.TenantID, session.ProjectID, session.ClusterID) == nil {
			visible = append(visible, session)
		}
	}
	return visible, nil
}

// Start persists the question synchronously, then detaches the loop from the
// request. Returning only after the input exists means a 202 never points at a
// job that vanished before it had a durable identity.
func (runtime *Runtime) Start(ctx context.Context, input StartInput) (aisession.Entry, error) {
	if strings.TrimSpace(input.Text) == "" || input.Now.IsZero() {
		return aisession.Entry{}, ErrInvalidInput
	}
	runtime.mu.Lock()
	running := runtime.jobs[input.SessionID] != nil
	runtime.mu.Unlock()
	if running {
		return aisession.Entry{}, ErrAlreadyRunning
	}
	session, err := runtime.Get(ctx, input.SessionID, input.UserID, input.Now)
	if err != nil {
		return aisession.Entry{}, err
	}
	if err := runtime.authorizeEvidence(
		ctx, input.UserID, session.TenantID, session.ProjectID, session.ClusterID, input.Evidence,
	); err != nil {
		return aisession.Entry{}, err
	}
	entry, err := runtime.sessions.StartTurn(ctx, aisession.StartTurnInput{
		SessionID: input.SessionID,
		Content:   aisession.Content{Text: strings.TrimSpace(input.Text), Evidence: input.Evidence},
		Now:       input.Now,
	})
	if errors.Is(err, aisession.ErrBusy) {
		return aisession.Entry{}, ErrAlreadyRunning
	}
	if err != nil {
		return aisession.Entry{}, err
	}
	jobContext, cancel := context.WithTimeout(runtime.base, runtime.turnTimeout)
	runtime.mu.Lock()
	runtime.jobs[input.SessionID] = cancel
	runtime.waiting.Add(1)
	runtime.mu.Unlock()
	runtime.stream.publish(input.SessionID, StreamEvent{Type: StreamEntries, Turn: entry.Turn})
	go runtime.run(jobContext, turnJob{
		sessionID: input.SessionID, userID: input.UserID, tenantID: session.TenantID,
		projectID: session.ProjectID, clusterID: session.ClusterID, turn: entry.Turn,
		title: session.Title, question: entry.Content.Text, attachments: input.Attachments,
	})
	return entry, nil
}

func (runtime *Runtime) Cancel(
	ctx context.Context, sessionID, userID string, now time.Time,
) error {
	if _, err := runtime.Get(ctx, sessionID, userID, now); err != nil {
		return err
	}
	runtime.mu.Lock()
	cancel := runtime.jobs[sessionID]
	runtime.mu.Unlock()
	if cancel == nil {
		return ErrNoRunningTurn
	}
	cancel()
	return nil
}

func (runtime *Runtime) Wait() { runtime.waiting.Wait() }

func (runtime *Runtime) Trajectory(
	ctx context.Context,
	query aisession.TrajectoryQuery,
) ([]aisession.Entry, error) {
	session, err := runtime.Get(ctx, query.SessionID, query.InitiatorUserID, query.Now)
	if err != nil {
		return nil, err
	}
	entries, err := runtime.sessions.Trajectory(ctx, query)
	if err != nil {
		return nil, err
	}
	for index := range entries {
		content := &entries[index].Content
		if len(content.Evidence) == 0 {
			continue
		}
		allowed := make([]aisession.Evidence, 0, len(content.Evidence))
		for _, evidence := range content.Evidence {
			if runtime.authorizeEvidence(ctx, query.InitiatorUserID, session.TenantID, session.ProjectID, session.ClusterID,
				[]aisession.Evidence{evidence}) == nil {
				if scope, scopeErr := runtime.authorizer.ResolveClusterScope(ctx, evidence.Cluster); scopeErr == nil {
					evidence.TenantID = scope.TenantID
					evidence.ProjectID = scope.ProjectID
				}
				allowed = append(allowed, evidence)
			}
		}
		if len(allowed) == 0 && (entries[index].Kind == aisession.KindContext ||
			entries[index].Kind == aisession.KindToolResult) {
			content.Text = "当前权限已无法读取这项集群证据"
		}
		content.Evidence = allowed
	}
	return entries, nil
}

// turnJob is everything the loop needs that does not change while it runs. The
// approval mode is deliberately absent: an operator may switch it mid-turn, so
// it is re-read from the session before every decision that depends on it.
type turnJob struct {
	sessionID string
	userID    string
	tenantID  string
	projectID string
	clusterID string
	turn      int32
	// title is what the session was called when this turn started. The first
	// turn renames a session that still carries the name the Console gave it,
	// and comparing against this is how it tells that apart from a name a
	// person chose in the meantime.
	title       string
	question    string
	attachments []aisession.Attachment
}

func (runtime *Runtime) run(ctx context.Context, job turnJob) {
	defer runtime.waiting.Done()
	defer func() {
		runtime.mu.Lock()
		delete(runtime.jobs, job.sessionID)
		runtime.mu.Unlock()
	}()
	if err := runtime.revalidate(ctx, job.userID, job.tenantID, job.projectID, job.clusterID); err != nil {
		runtime.fail(job.sessionID, aisession.FailurePermissionRevoked)
		return
	}
	settings, err := runtime.model.Get(ctx)
	if err != nil || !settings.Enabled {
		runtime.fail(job.sessionID, aisession.FailureModelUnavailable)
		return
	}
	runtime.nameSession(ctx, job)
	specs := runtime.ToolCatalogue()
	mode := runtime.currentMode(ctx, job)
	runtime.append(ctx, job.sessionID, aisession.AppendInput{
		Kind: aisession.KindSystem,
		Content: aisession.Content{
			Text:  runtimeContextText(job.clusterID, mode, specs),
			Mode:  mode,
			Tools: specNames(specs),
		},
		OccurredAt: time.Now().UTC(),
	})
	for _, attachment := range job.attachments {
		for _, chunk := range attachmentChunks(attachment) {
			runtime.append(ctx, job.sessionID, aisession.AppendInput{
				Kind: aisession.KindContext, Content: aisession.Content{Text: chunk},
				OccurredAt: time.Now().UTC(),
			})
		}
	}
	runtime.loop(ctx, job, settings, specs)
}

// loop is the agent itself: think, act, observe, repeat.
//
// Every iteration rebuilds the model input from the durable trail rather than
// carrying a mutable message list, so a permission that was revoked between
// steps stops being readable at the next step, not at the next session.
func (runtime *Runtime) loop(
	ctx context.Context,
	job turnJob,
	settings aimodel.Settings,
	specs []ToolSpec,
) {
	definitions := toolDefinitions(specs)
	budget := runtime.budgetFor(settings)
	toolCalls := 0
	repeats := make(map[string]int)
	for step := 1; step <= runtime.maxSteps; step++ {
		if ctx.Err() != nil {
			runtime.fail(job.sessionID, cancellationFailure(ctx))
			return
		}
		if err := runtime.revalidate(ctx, job.userID, job.tenantID, job.projectID, job.clusterID); err != nil {
			runtime.fail(job.sessionID, aisession.FailurePermissionRevoked)
			return
		}
		mode := runtime.currentMode(ctx, job)
		system := systemPrompt(job.clusterID, mode, specs)
		completion, evidence, pressure, err := runtime.think(
			ctx, job, step, budget, system, definitions, specs,
		)
		if err != nil {
			runtime.fail(job.sessionID, failureFor(ctx, err))
			return
		}
		tokens := &aisession.Tokens{
			Input: completion.Usage.InputTokens, CachedInput: completion.Usage.CachedInputTokens,
			Output: completion.Usage.OutputTokens, Reasoning: completion.Usage.ReasoningTokens,
			Context: pressure.TotalTokens, ContextWindow: budget.contextWindowTokens,
		}
		timing := &aisession.Timing{
			FirstTokenMS: int(completion.FirstToken.Milliseconds()),
			ElapsedMS:    int(completion.Elapsed.Milliseconds()),
			Streamed:     completion.Streamed,
		}
		if reasoning := strings.TrimSpace(completion.Reasoning); reasoning != "" {
			runtime.append(ctx, job.sessionID, aisession.AppendInput{
				Kind:       aisession.KindReasoning,
				Content:    aisession.Content{Text: reasoning, Step: step},
				OccurredAt: time.Now().UTC(),
			})
		}
		requested := make([]string, 0, len(completion.ToolCalls))
		for _, call := range completion.ToolCalls {
			requested = append(requested, call.Name)
		}
		runtime.append(ctx, job.sessionID, aisession.AppendInput{
			Kind: aisession.KindModel,
			Content: aisession.Content{
				Text: completion.Text, Step: step, Tokens: tokens, Timing: timing, Tools: requested,
			},
			OccurredAt: time.Now().UTC(), Duration: completion.Elapsed,
		})
		if len(completion.ToolCalls) == 0 {
			runtime.conclude(ctx, job, step, completion.Text, evidence, tokens)
			return
		}
		if toolCalls+len(completion.ToolCalls) > runtime.maxToolCalls {
			runtime.fail(job.sessionID, aisession.FailureToolBudget)
			return
		}
		toolCalls += len(completion.ToolCalls)
		if !runtime.runToolCalls(ctx, job, step, completion.ToolCalls, specs, mode, repeats) {
			return
		}
	}
	runtime.fail(job.sessionID, aisession.FailureStepBudget)
}

// think produces one model step, compacting and retrying as the endpoint
// requires.
//
// Three different failures are handled here rather than in the loop because
// they are all answers to the same question — can this request be made at all —
// and the loop's job is what to do with a step that succeeded. A transient
// endpoint failure is retried with backoff; a request the endpoint refuses as
// too large is compacted and sent again; a conversation that cannot be reduced
// far enough ends the turn instead of asking again for the same rejection.
func (runtime *Runtime) think(
	ctx context.Context,
	job turnJob,
	step int,
	budget contextBudget,
	system string,
	definitions []aimodel.ToolDefinition,
	specs []ToolSpec,
) (aimodel.Completion, []aisession.Evidence, Pressure, error) {
	for overflow := 0; ; overflow++ {
		messages, evidence, pressure, err := runtime.prepare(
			ctx, job, budget, step, system, definitions, specs,
		)
		if err != nil {
			return aimodel.Completion{}, nil, Pressure{}, err
		}
		completion, err := runtime.complete(ctx, job, step, aimodel.CompletionInput{
			System:          system,
			Messages:        messages,
			Tools:           definitions,
			MaxOutputTokens: budget.maxOutputTokens,
			OnDelta: func(delta aimodel.Delta) {
				runtime.publishDelta(job, step, delta)
			},
		})
		if err == nil {
			return completion, evidence, pressure, nil
		}
		if !aimodel.IsContextOverflow(err) || overflow >= runtime.compaction.MaxOverflowRetries {
			return aimodel.Completion{}, nil, Pressure{}, err
		}
		// The endpoint has just told us its own accounting disagrees with ours.
		// Compacting is the only thing that changes the answer, and a
		// conversation that cannot be compacted has to say so rather than send
		// the same request again.
		entries, historyErr := runtime.loadHistory(ctx, job.sessionID, job.userID)
		if historyErr != nil {
			return aimodel.Completion{}, nil, Pressure{}, historyErr
		}
		if !runtime.compact(
			ctx, job, entries, budget, aisession.CompactionTriggerOverflow, step, specs,
		) {
			return aimodel.Completion{}, nil, Pressure{}, ErrContextBudget
		}
	}
}

// complete sends one request, retrying the failures that a second attempt can
// actually fix.
//
// Rate limits and 5xx from a shared endpoint are the ordinary weather of a
// deployment that does not own its inference service, and ending a turn on one
// throws away everything the turn had done so far. Backoff is exponential and
// jittered so several turns that were refused together do not come back
// together. Anything the endpoint would refuse identically — a bad credential,
// an exhausted balance, a request it will not accept — is returned at once.
func (runtime *Runtime) complete(
	ctx context.Context, job turnJob, step int, input aimodel.CompletionInput,
) (aimodel.Completion, error) {
	streamed := false
	delegate := input.OnDelta
	input.OnDelta = func(delta aimodel.Delta) {
		streamed = true
		if delegate != nil {
			delegate(delta)
		}
	}
	delay := runtime.retry.InitialDelay
	var lastErr error
	for attempt := 0; attempt <= runtime.retry.MaxRetries; attempt++ {
		if attempt > 0 {
			// A retried call starts its answer over. Whatever the failed
			// attempt put on screen has to go, or the operator reads one answer
			// spliced onto the beginning of another.
			if streamed {
				runtime.stream.publish(job.sessionID, StreamEvent{
					Type: StreamReset, Turn: job.turn, Step: step,
				})
				streamed = false
			}
			if !sleepFor(ctx, backoffFor(delay, runtime.retry.JitterRatio)) {
				return aimodel.Completion{}, ctx.Err()
			}
			if delay < runtime.retry.MaxDelay {
				delay = min(delay*2, runtime.retry.MaxDelay)
			}
		}
		completion, _, err := runtime.model.Complete(ctx, input)
		if err == nil {
			return completion, nil
		}
		lastErr = err
		if ctx.Err() != nil || !aimodel.IsRetryable(err) {
			return aimodel.Completion{}, err
		}
	}
	return aimodel.Completion{}, lastErr
}

// backoffFor spreads one delay around its nominal value.
//
// The jitter is symmetric and derived from the clock rather than from a seeded
// generator: nothing here needs to be reproducible, and the only property that
// matters is that two Servers refused at the same instant do not come back at
// the same instant.
func backoffFor(delay time.Duration, ratio float64) time.Duration {
	if ratio <= 0 {
		return delay
	}
	spread := float64(delay) * ratio
	offset := (float64(time.Now().UnixNano()%2001)/1000 - 1) * spread
	jittered := time.Duration(float64(delay) + offset)
	if jittered < 0 {
		return 0
	}
	return jittered
}

// sleepFor waits, and reports whether the wait finished rather than the turn.
func sleepFor(ctx context.Context, delay time.Duration) bool {
	if delay <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// prepare rebuilds the model input and compacts it when it no longer fits.
func (runtime *Runtime) prepare(
	ctx context.Context,
	job turnJob,
	budget contextBudget,
	step int,
	system string,
	definitions []aimodel.ToolDefinition,
	specs []ToolSpec,
) ([]aimodel.Message, []aisession.Evidence, Pressure, error) {
	entries, err := runtime.loadHistory(ctx, job.sessionID, job.userID)
	if err != nil {
		return nil, nil, Pressure{}, err
	}
	pressure := measure(entries, system, definitions)
	if pressure.TotalTokens >= budget.thresholdTokens {
		if runtime.compact(
			ctx, job, entries, budget, aisession.CompactionTriggerPressure, step, specs,
		) {
			if entries, err = runtime.loadHistory(ctx, job.sessionID, job.userID); err != nil {
				return nil, nil, Pressure{}, err
			}
			pressure = measure(entries, system, definitions)
		}
	}
	if pressure.TotalTokens+budget.maxOutputTokens >= budget.contextWindowTokens {
		return nil, nil, Pressure{}, ErrContextBudget
	}
	messages, _ := buildMessages(entries)
	// The conclusion cites what this turn read, not everything the session ever
	// read. Carrying the whole history's references made every answer end in
	// the same growing wall of chips — including answers to "谢谢你", which read
	// nothing at all and cited ten objects anyway.
	return messages, turnEvidence(entries, job.turn), pressure, nil
}

// ContextUsage reports how much of the model's context window this session
// currently occupies.
//
// The composer shows it so an operator can see a long investigation approaching
// the point where it will be compacted, rather than discovering it from a
// checkpoint appearing in the trail. It is computed the same way the loop
// computes it before every request, because a meter that disagreed with the
// thing it measures would be worse than no meter.
type ContextUsage struct {
	UsedTokens          int
	ContextWindowTokens int
	ThresholdTokens     int
	SystemTokens        int
	ToolsTokens         int
	MessageTokens       int
	// Measured reports whether the total is anchored on the endpoint's own
	// accounting rather than estimated end to end.
	Measured bool
}

func (runtime *Runtime) Usage(
	ctx context.Context, sessionID, userID string, now time.Time,
) (ContextUsage, error) {
	session, err := runtime.Get(ctx, sessionID, userID, now)
	if err != nil {
		return ContextUsage{}, err
	}
	settings, err := runtime.model.Get(ctx)
	if err != nil {
		return ContextUsage{}, err
	}
	if !settings.Enabled || settings.ContextWindowTokens <= 0 {
		return ContextUsage{}, ErrModelNotReady
	}
	entries, err := runtime.loadHistory(ctx, sessionID, userID)
	if err != nil {
		return ContextUsage{}, err
	}
	specs := runtime.ToolCatalogue()
	budget := runtime.budgetFor(settings)
	pressure := measure(
		entries,
		systemPrompt(session.ClusterID, session.ApprovalMode, specs),
		toolDefinitions(specs),
	)
	return ContextUsage{
		UsedTokens:          pressure.TotalTokens,
		ContextWindowTokens: budget.contextWindowTokens,
		ThresholdTokens:     budget.thresholdTokens,
		SystemTokens:        pressure.SystemTokens,
		ToolsTokens:         pressure.ToolsTokens,
		MessageTokens:       pressure.MessageTokens,
		Measured:            pressure.Measured,
	}, nil
}

// plannedCall is one requested tool call and what the runtime decided about it
// before anything was executed.
type plannedCall struct {
	call aimodel.ToolCall
	spec ToolSpec
	step int
	// arguments is the decoded call body, set only when the call will run.
	arguments []byte
	target    *aisession.Target
	// outcome is the result to record when the call will not run: an unknown
	// name, a missing permission, a refusal, a malformed body, or a repeat.
	outcome  *aisession.Content
	duration time.Duration
	run      bool
}

// runToolCalls admits, executes and records everything one step asked for.
//
// The three phases are separate because they answer to different rules.
// Admission is sequential and in the order the model asked, because a person
// answering approvals answers one at a time and an operator reading the trail
// has to see the calls in the order the model made them. Execution is
// concurrent and bounded when the whole batch is read-only. A batch containing
// a write runs serially in model order: concurrent writes make the outcome
// depend on scheduling, and a read beside a write would have no defined side
// of the change to observe. Recording is back in model order, so the trail and
// the export do not depend on which read happened to finish first.
//
// It reports whether the loop may continue; a false result means the turn
// already ended.
func (runtime *Runtime) runToolCalls(
	ctx context.Context,
	job turnJob,
	step int,
	calls []aimodel.ToolCall,
	specs []ToolSpec,
	mode aisession.ApprovalMode,
	repeats map[string]int,
) bool {
	planned := make([]plannedCall, 0, len(calls))
	for _, call := range calls {
		admitted, ended := runtime.admit(ctx, job, step, call, specs, mode, repeats)
		if ended {
			return false
		}
		planned = append(planned, admitted)
	}
	runtime.execute(ctx, job, planned)
	for _, item := range planned {
		outcome := item.outcome
		if outcome == nil {
			// Nothing should reach here without a decision, and a call the trail
			// records without a result is a hole in the record rather than a
			// silent success.
			outcome = &aisession.Content{Text: "这次调用没有产生结果。", Failed: true}
		}
		runtime.recordResult(ctx, job, step, item.call, item.target, *outcome, item.duration)
	}
	return true
}

// admit decides one call: is it a tool, may this operator use it, does a person
// have to say yes first, and are its arguments usable.
//
// Every branch that refuses records the refusal as the call's result, so the
// model is told what happened and can change course. The one branch that ends
// the turn is an approval nobody answered or a cancellation, because neither
// leaves anything for the model to do next.
func (runtime *Runtime) admit(
	ctx context.Context,
	job turnJob,
	step int,
	call aimodel.ToolCall,
	specs []ToolSpec,
	mode aisession.ApprovalMode,
	repeats map[string]int,
) (plannedCall, bool) {
	item := plannedCall{
		call: call, step: step, target: &aisession.Target{Cluster: job.clusterID},
	}
	spec, known := findSpec(specs, call.Name)
	if !known {
		runtime.recordCall(ctx, job, step, call, item.target, false)
		item.outcome = &aisession.Content{
			Text: fmt.Sprintf("没有名为 %s 的工具。可用工具：%s。",
				call.Name, strings.Join(specNames(specs), ", ")),
			Failed: true,
		}
		return item, false
	}
	item.spec = spec
	if spec.Target != nil {
		if target := spec.Target(json.RawMessage(call.Arguments)); target != nil {
			target.Cluster = job.clusterID
			item.target = target
		}
	}
	// Authorization is the operator current RBAC, checked here for this call
	// and not inherited from the session or from an earlier call that passed.
	authorized, missing := runtime.authorizeTool(ctx, job, spec)
	if !authorized {
		runtime.recordCall(ctx, job, step, call, item.target, false)
		runtime.recordAudit(
			ctx, job, step, call, spec, item.target, auditResultDenied, string(missing),
		)
		item.outcome = &aisession.Content{
			Text:   fmt.Sprintf("当前账户在该 Cluster 上没有 %s 权限，未执行这次调用。", missing),
			Failed: true,
		}
		return item, false
	}
	if requiresApprovalFor(spec, mode, json.RawMessage(call.Arguments)) {
		decision, failure := runtime.awaitApproval(ctx, job, step, call, spec, mode, item.target)
		if failure != "" {
			runtime.fail(job.sessionID, failure)
			return item, true
		}
		if decision == aisession.DecisionDenied {
			runtime.recordCall(ctx, job, step, call, item.target, false)
			item.outcome = &aisession.Content{
				Text: "用户拒绝了这次调用。请在不执行它的前提下继续，或说明为什么无法继续。", Failed: true,
			}
			return item, false
		}
		// Approval can wait for minutes. Neither an earlier permission decision nor
		// an approval is authority to run after the account or RBAC changed while
		// the turn was parked.
		if err := runtime.revalidate(ctx, job.userID, job.tenantID, job.projectID, job.clusterID); err != nil {
			runtime.fail(job.sessionID, aisession.FailurePermissionRevoked)
			return item, true
		}
		authorized, missing = runtime.authorizeTool(ctx, job, spec)
		if !authorized {
			runtime.recordCall(ctx, job, step, call, item.target, false)
			runtime.recordAudit(
				ctx, job, step, call, spec, item.target, auditResultDenied, string(missing),
			)
			item.outcome = &aisession.Content{
				Text: fmt.Sprintf(
					"批准后重新检查发现当前账户已没有 %s 权限，未执行这次调用。",
					missing,
				),
				Failed: true,
			}
			return item, false
		}
	}
	runtime.recordCall(ctx, job, step, call, item.target, true)
	arguments, argumentsErr := decodeArguments(call.Arguments)
	if argumentsErr != nil {
		item.outcome = &aisession.Content{
			Text:   "参数不是合法的 JSON 对象，请按工具 Schema 重新构造后再调用。",
			Failed: true,
		}
		return item, false
	}
	fingerprint := call.Name + "\x00" + string(arguments)
	repeats[fingerprint]++
	if repeats[fingerprint] > runtime.repeatedCallLimit {
		item.outcome = &aisession.Content{
			Text:   "同样的调用在本轮中已经执行过，结果不会改变。请改变参数或基于已有结果给出结论。",
			Failed: true,
		}
		return item, false
	}
	item.arguments = arguments
	item.run = true
	return item, false
}

// execute runs an all-read batch with bounded concurrency. The presence of one
// mutating call makes the entire batch serial: model order is then the only
// safe and explainable order for both writes and adjacent reads.
func (runtime *Runtime) execute(ctx context.Context, job turnJob, planned []plannedCall) {
	for index := range planned {
		if planned[index].run && planned[index].spec.Mutating {
			for itemIndex := range planned {
				if planned[itemIndex].run {
					runtime.invoke(ctx, job, &planned[itemIndex])
				}
			}
			return
		}
	}
	slots := make(chan struct{}, runtime.maxParallelToolCalls)
	var running sync.WaitGroup
	for index := range planned {
		if !planned[index].run {
			continue
		}
		running.Add(1)
		go func(item *plannedCall) {
			defer running.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			runtime.invoke(ctx, job, item)
		}(&planned[index])
	}
	running.Wait()
}

// invoke performs one authorized call and turns it into the content the trail
// and the model will both see.
func (runtime *Runtime) invoke(ctx context.Context, job turnJob, item *plannedCall) {
	started := time.Now()
	result, toolErr := runtime.tools.Invoke(ctx, ToolInvocation{
		Name: item.call.Name, ClusterID: job.clusterID,
		UserID: job.userID, Arguments: item.arguments,
		IdempotencyKey: toolIdempotencyKey(job, item.step, item.call.ID),
	})
	content := aisession.Content{Untrusted: true, Target: item.target}
	auditResult := auditResultSucceeded
	if toolErr != nil {
		content.Failed = true
		content.Text = toolFailureText(toolErr)
		auditResult = auditResultFailed
	} else {
		content.Text = result.Text
		content.Failed = result.Failed || result.Denied
		if result.Denied {
			auditResult = auditResultDenied
		} else if result.Failed {
			auditResult = auditResultFailed
		}
		content.Evidence = runtime.visibleEvidence(ctx, job, result.Evidence)
		if result.Target != nil {
			result.Target.Cluster = job.clusterID
			content.Target = result.Target
		}
	}
	item.target = content.Target
	if toolErr == nil && len(result.AuditTargets) > 0 {
		for _, auditTarget := range result.AuditTargets {
			target := auditTarget.Target
			target.Cluster = job.clusterID
			resolvedResult := auditTarget.Result
			switch resolvedResult {
			case auditResultSucceeded, auditResultFailed, auditResultDenied:
			default:
				resolvedResult = auditResult
			}
			runtime.recordAudit(
				ctx, job, item.step, item.call, item.spec, &target,
				resolvedResult, auditTarget.MissingPermission,
			)
		}
	} else {
		runtime.recordAudit(
			ctx, job, item.step, item.call, item.spec, item.target, auditResult, "",
		)
	}
	item.outcome = &content
	item.duration = time.Since(started)
}

// toolIdempotencyKey is stable for one persisted turn and opaque to the Agent.
// Hashing also keeps model-controlled call identifiers out of the key grammar
// and within the shared idempotency-key bound.
func toolIdempotencyKey(job turnJob, step int, callID string) string {
	identity := fmt.Sprintf("%s\x00%d\x00%d\x00%s", job.sessionID, job.turn, step, callID)
	digest := sha256.Sum256([]byte(identity))
	return fmt.Sprintf("aiops:%x", digest[:])
}

// Audit results, as the audit store spells them.
const (
	auditResultSucceeded = "succeeded"
	auditResultFailed    = "failed"
	auditResultDenied    = "denied"
)

// recordAudit writes one tool call into the deployment audit trail.
//
// The request identifier names the step rather than an HTTP request, because
// there is not one: the loop runs after the request that started it returned.
// Pointing at the session, turn and step is what lets an auditor go from the
// audit row to the exact place in the trail that explains it.
//
// Failures to record are dropped rather than propagated: this runs after the
// tool already happened, and turning an audit outage into a failed turn would
// report a write as failed even when the Cluster accepted it. Deployment audit
// health must be monitored separately from the operation result.
func (runtime *Runtime) recordAudit(
	ctx context.Context,
	job turnJob,
	step int,
	call aimodel.ToolCall,
	spec ToolSpec,
	target *aisession.Target,
	result, missing string,
) {
	if runtime.audit == nil {
		return
	}
	detail := map[string]string{
		"tool":       call.Name,
		"session_id": job.sessionID,
		"turn":       strconv.Itoa(int(job.turn)),
		"step":       strconv.Itoa(step),
		"mutating":   strconv.FormatBool(spec.Mutating),
		"sensitive": strconv.FormatBool(
			spec.Sensitive || (spec.SensitiveWhen != nil &&
				spec.SensitiveWhen(json.RawMessage(call.Arguments))),
		),
	}
	if target != nil {
		detail["namespace"] = target.Namespace
		detail["gvk"] = target.GVK
		detail["resource_name"] = target.Name
	}
	if missing != "" {
		detail["missing_permission"] = missing
	}
	_ = runtime.audit.RecordClusterEvent(context.WithoutCancel(ctx), audit.ClusterEventInput{
		ActorUserID: job.userID,
		ClusterID:   job.clusterID,
		Action:      auditaction.AIToolInvoke,
		TargetType:  auditaction.TargetAISession,
		TargetID:    job.sessionID,
		TargetName:  call.Name,
		Result:      result,
		RequestID:   fmt.Sprintf("aiops:%s:%d:%d", job.sessionID, job.turn, step),
		Detail:      detail,
	})
}

// awaitApproval parks the turn on a person and records both halves of the
// exchange. It returns the decision, or a failure classification when nobody
// answered.
func (runtime *Runtime) awaitApproval(
	ctx context.Context,
	job turnJob,
	step int,
	call aimodel.ToolCall,
	spec ToolSpec,
	mode aisession.ApprovalMode,
	target *aisession.Target,
) (string, string) {
	answer := runtime.approvals.open(job.sessionID, call.ID)
	defer runtime.approvals.close(job.sessionID, call.ID)
	runtime.append(ctx, job.sessionID, aisession.AppendInput{
		Kind: aisession.KindApprovalRequest,
		Content: aisession.Content{
			Tool: call.Name, CallID: call.ID, Arguments: call.Arguments, Step: step,
			Target: target, Mode: mode, Text: spec.Description,
		},
		OccurredAt: time.Now().UTC(),
	})
	timeout := time.NewTimer(runtime.approvalTimeout)
	defer timeout.Stop()
	var decision string
	select {
	case decision = <-answer:
	case <-ctx.Done():
		return "", cancellationFailure(ctx)
	case <-timeout.C:
		return "", aisession.FailureApprovalTimeout
	}
	runtime.append(ctx, job.sessionID, aisession.AppendInput{
		Kind: aisession.KindApprovalDecision,
		Content: aisession.Content{
			Tool: call.Name, CallID: call.ID, Step: step, Decision: decision, Target: target,
		},
		OccurredAt: time.Now().UTC(),
	})
	return decision, ""
}

// nameSession gives a new conversation a name that says what it is about.
//
// It runs beside the turn rather than inside it: naming is a convenience, and
// an operator should not wait for it to see the first tool call. Only the first
// turn names a session, only when the title is still the one the Console
// generated, and a model that does not answer in time leaves the question
// itself as the name — a list row that says too much is still better than one
// that says only when it was opened.
func (runtime *Runtime) nameSession(ctx context.Context, job turnJob) {
	if job.turn != 1 || strings.TrimSpace(job.question) == "" {
		return
	}
	runtime.waiting.Add(1)
	go func() {
		defer runtime.waiting.Done()
		// Detached from the turn: cancelling a run does not un-ask the question,
		// and the name is as useful on a conversation that was stopped.
		named, cancel := context.WithTimeout(
			context.WithoutCancel(ctx), runtime.titleTimeout)
		defer cancel()
		title := runtime.proposeTitle(named, job.question)
		// Re-read rather than rename straight away: an operator who named the
		// conversation themselves while the first turn ran has said what it is
		// called, and a model is not entitled to a second opinion.
		current, err := runtime.sessions.Get(named, job.sessionID, job.userID, time.Now().UTC())
		if err != nil || current.Title != job.title {
			return
		}
		session, err := runtime.sessions.Rename(named, job.sessionID, job.userID, title, time.Now().UTC())
		if err != nil || session.Title == "" {
			return
		}
		runtime.stream.publish(job.sessionID, StreamEvent{Type: StreamEntries, Turn: job.turn})
	}()
}

// proposeTitle asks the model for a name and falls back to the question.
func (runtime *Runtime) proposeTitle(ctx context.Context, question string) string {
	fallback := trimRunes(strings.Join(strings.Fields(question), " "), maxTitleRunes)
	completion, _, err := runtime.model.Complete(ctx, aimodel.CompletionInput{
		System:          titlePrompt,
		Messages:        []aimodel.Message{{Role: aimodel.RoleUser, Text: titleRequest(question)}},
		MaxOutputTokens: maxTitleOutputTokens,
	})
	if err != nil {
		return fallback
	}
	// Model output is text from outside ZKE landing in a list row: one line,
	// no decoration it wrapped around the answer, and bounded length.
	title := strings.TrimSpace(completion.Text)
	if index := strings.IndexAny(title, "\r\n"); index >= 0 {
		title = strings.TrimSpace(title[:index])
	}
	title = strings.Trim(title, " \t\"'`《》「」“”‘’")
	if title == "" {
		return fallback
	}
	return trimRunes(title, maxTitleRunes)
}

func trimRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return strings.TrimSpace(string(runes[:limit]))
}

func (runtime *Runtime) conclude(
	ctx context.Context,
	job turnJob,
	step int,
	text string,
	evidence []aisession.Evidence,
	tokens *aisession.Tokens,
) {
	runtime.append(ctx, job.sessionID, aisession.AppendInput{
		Kind: aisession.KindConclusion,
		Content: aisession.Content{
			Text: text, Step: step, Evidence: evidence, Tokens: tokens,
		},
		OccurredAt: time.Now().UTC(),
	})
	_, _ = runtime.sessions.FinishTurn(context.WithoutCancel(ctx), aisession.FinishTurnInput{
		SessionID: job.sessionID, Status: aisession.TurnSucceeded, Now: time.Now().UTC(),
	})
	runtime.stream.publish(job.sessionID, StreamEvent{Type: StreamEntries, Turn: job.turn})
}

func (runtime *Runtime) recordCall(
	ctx context.Context, job turnJob, step int,
	call aimodel.ToolCall, target *aisession.Target, authorized bool,
) {
	decision := authorized
	runtime.append(ctx, job.sessionID, aisession.AppendInput{
		Kind: aisession.KindToolCall,
		Content: aisession.Content{
			Tool: call.Name, CallID: call.ID, Arguments: call.Arguments,
			Step: step, Target: target, Authorized: &decision,
		},
		OccurredAt: time.Now().UTC(),
	})
}

func (runtime *Runtime) recordResult(
	ctx context.Context, job turnJob, step int, call aimodel.ToolCall,
	target *aisession.Target, content aisession.Content, duration time.Duration,
) {
	content.Tool = call.Name
	content.CallID = call.ID
	content.Step = step
	if content.Target == nil {
		content.Target = target
	}
	runtime.append(ctx, job.sessionID, aisession.AppendInput{
		Kind: aisession.KindToolResult, Content: content,
		OccurredAt: time.Now().UTC(), Duration: duration,
	})
}

// append writes one entry and only then tells watchers to read it. The order is
// the invariant: a watcher never learns about something the trail does not have.
func (runtime *Runtime) append(
	ctx context.Context, sessionID string, input aisession.AppendInput,
) {
	input.SessionID = sessionID
	entry, err := runtime.sessions.Append(context.WithoutCancel(ctx), input)
	if err != nil {
		return
	}
	runtime.stream.publish(sessionID, StreamEvent{
		Type: StreamEntries, Turn: entry.Turn, Step: input.Content.Step,
	})
}

func (runtime *Runtime) publishDelta(job turnJob, step int, delta aimodel.Delta) {
	if delta.Text != "" {
		runtime.stream.publish(job.sessionID, StreamEvent{
			Type: StreamDelta, Turn: job.turn, Step: step, Text: delta.Text,
		})
	}
	if delta.Reasoning != "" {
		runtime.stream.publish(job.sessionID, StreamEvent{
			Type: StreamReasoning, Turn: job.turn, Step: step, Text: delta.Reasoning,
		})
	}
}

// currentMode re-reads the approval mode the session is in right now. An
// operator who switches while a turn runs expects the next decision to use the
// new mode, which is the whole point of having the control in the composer.
func (runtime *Runtime) currentMode(ctx context.Context, job turnJob) aisession.ApprovalMode {
	session, err := runtime.sessions.Get(ctx, job.sessionID, job.userID, time.Now().UTC())
	if err != nil || !session.ApprovalMode.Valid() {
		return aisession.ApprovalAsk
	}
	return session.ApprovalMode
}

// authorizeTool checks every permission a tool declares, and reports the first
// one the operator is missing so the model can be told what it was refused.
func (runtime *Runtime) authorizeTool(
	ctx context.Context, job turnJob, spec ToolSpec,
) (bool, rbac.Permission) {
	for _, permission := range spec.Permissions {
		resolved, err := runtime.authorizer.AuthorizeCluster(ctx, job.userID, permission, job.clusterID)
		if err != nil || resolved.TenantID != job.tenantID || resolved.ProjectID != job.projectID {
			return false, permission
		}
	}
	return len(spec.Permissions) > 0, ""
}

// visibleEvidence keeps only what the operator may still read. A tool that
// returned a reference the caller cannot open would put a dead link in the
// trail and a claim of access in the record.
func (runtime *Runtime) visibleEvidence(
	ctx context.Context, job turnJob, evidence []aisession.Evidence,
) []aisession.Evidence {
	allowed := make([]aisession.Evidence, 0, len(evidence))
	for _, item := range evidence {
		item.Cluster = job.clusterID
		if runtime.authorizeEvidence(ctx, job.userID, job.tenantID, job.projectID, job.clusterID,
			[]aisession.Evidence{item}) == nil {
			allowed = append(allowed, item)
		}
	}
	return allowed
}

func (runtime *Runtime) fail(sessionID, failure string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(runtime.base), 5*time.Second)
	defer cancel()
	_, _ = runtime.sessions.Append(ctx, aisession.AppendInput{
		SessionID: sessionID, Kind: aisession.KindError,
		Content: aisession.Content{Failure: failure}, OccurredAt: time.Now().UTC(),
	})
	status := aisession.TurnFailed
	if failure == aisession.FailureSessionEnded {
		status = aisession.TurnCanceled
		failure = ""
	}
	_, _ = runtime.sessions.FinishTurn(ctx, aisession.FinishTurnInput{
		SessionID: sessionID, Status: status, Failure: failure, Now: time.Now().UTC(),
	})
	runtime.stream.publish(sessionID, StreamEvent{Type: StreamEntries})
}

func (runtime *Runtime) loadHistory(ctx context.Context, sessionID, userID string) ([]aisession.Entry, error) {
	result := make([]aisession.Entry, 0)
	var after int32
	for {
		page, err := runtime.Trajectory(ctx, aisession.TrajectoryQuery{
			SessionID: sessionID, InitiatorUserID: userID, AfterSequence: after,
			Limit: trajectoryPageSize, Now: time.Now().UTC(),
		})
		if err != nil {
			return nil, err
		}
		result = append(result, page...)
		if len(page) < trajectoryPageSize {
			return result, nil
		}
		after = page[len(page)-1].Sequence
	}
}

func (runtime *Runtime) revalidate(
	ctx context.Context, userID, tenantID, projectID, clusterID string,
) error {
	active, err := runtime.users.IsAIUserActive(ctx, userID)
	if err != nil {
		return err
	}
	if !active {
		return ErrUserInactive
	}
	return runtime.AuthorizeTarget(ctx, userID, tenantID, projectID, clusterID)
}

func (runtime *Runtime) AuthorizeTarget(
	ctx context.Context, userID, tenantID, projectID, clusterID string,
) error {
	if !validation.IsUUID(userID) || !validation.IsUUID(tenantID) ||
		!validation.IsUUID(projectID) || !validation.IsUUID(clusterID) {
		return ErrInvalidInput
	}
	resolved, err := runtime.authorizer.AuthorizeCluster(ctx, userID, rbac.PermissionAIRun, clusterID)
	if errors.Is(err, rbac.ErrDenied) || errors.Is(err, rbac.ErrInvalidScope) ||
		(err == nil && (resolved.TenantID != tenantID || resolved.ProjectID != projectID)) {
		return ErrForbidden
	}
	return err
}

func (runtime *Runtime) authorizeEvidence(
	ctx context.Context, userID, tenantID, projectID, clusterID string,
	evidence []aisession.Evidence,
) error {
	for _, item := range evidence {
		if item.Cluster != clusterID {
			return ErrForbidden
		}
		permission := rbac.PermissionClusterRead
		switch item.Kind {
		case aisession.EvidenceEvent:
			permission = rbac.PermissionClusterEventRead
		case aisession.EvidenceMetric:
			permission = rbac.PermissionClusterMetricsRead
		case aisession.EvidenceLog:
			permission = rbac.PermissionClusterPodLogsRead
		case aisession.EvidenceResource:
		default:
			return ErrInvalidInput
		}
		resolved, err := runtime.authorizer.AuthorizeCluster(ctx, userID, permission, item.Cluster)
		if err != nil || resolved.TenantID != tenantID || resolved.ProjectID != projectID {
			return ErrForbidden
		}
	}
	return nil
}

func toolDefinitions(specs []ToolSpec) []aimodel.ToolDefinition {
	definitions := make([]aimodel.ToolDefinition, 0, len(specs))
	for _, spec := range specs {
		definitions = append(definitions, aimodel.ToolDefinition{
			Name: spec.Name, Description: spec.Description, Parameters: spec.Schema,
		})
	}
	return definitions
}

// decodeArguments accepts only a JSON object. A model that sent something else
// gets told so and can correct itself, which is better than a tool guessing
// what an array or a bare string was supposed to mean.
func decodeArguments(value string) (json.RawMessage, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return json.RawMessage("{}"), nil
	}
	var probe map[string]any
	if err := json.Unmarshal([]byte(trimmed), &probe); err != nil {
		return nil, err
	}
	return json.RawMessage(trimmed), nil
}

// toolFailureText is what the model is told about a failed call.
//
// The classification, never the underlying error string: that text can carry
// an address, a header or a fragment of a credential, and it would end up in
// both the model context and the durable trail.
func toolFailureText(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "工具执行超时，未拿到结果。可以缩小查询范围后重试。"
	case errors.Is(err, context.Canceled):
		return "工具执行已取消。"
	case errors.Is(err, ErrInvalidInput):
		return "参数不符合工具 Schema，请修正后重试。"
	default:
		return "工具执行失败，可能是目标 Cluster Agent 暂时不可达或对象不存在。请据此调整下一步。"
	}
}

func cancellationFailure(ctx context.Context) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return aisession.FailureModelTimeout
	}
	return aisession.FailureSessionEnded
}

// failureFor classifies why a step could not be produced.
//
// The model's own classification is used where it exists: a rate limit that
// outlasted every retry, an exhausted balance and an endpoint that never
// answered are three different things for an operator to go and fix, and
// reporting all of them as "unavailable" would make the first two look like a
// network problem.
func failureFor(ctx context.Context, err error) string {
	if errors.Is(err, ErrContextBudget) {
		return aisession.FailureBudgetExceeded
	}
	if errors.Is(err, ErrForbidden) || errors.Is(err, ErrUserInactive) {
		return aisession.FailurePermissionRevoked
	}
	if ctx.Err() != nil {
		return cancellationFailure(ctx)
	}
	switch aimodel.FailureOf(err) {
	case aimodel.CallTimeout:
		return aisession.FailureModelTimeout
	case aimodel.CallRateLimited:
		return aisession.FailureModelRateLimited
	case aimodel.CallQuota:
		return aisession.FailureModelQuota
	case aimodel.CallAuth, aimodel.CallInvalidRequest:
		return aisession.FailureModelRejected
	case aimodel.CallContextOverflow:
		return aisession.FailureBudgetExceeded
	default:
		return aisession.FailureModelUnavailable
	}
}

func attachmentChunks(attachment aisession.Attachment) []string {
	const chunkBytes = 7 * 1024
	prefix := fmt.Sprintf("附件 %s (%s)：\n", attachment.Name, attachment.MediaType)
	content := attachment.Content
	result := make([]string, 0, len(content)/chunkBytes+1)
	for len(content) > 0 {
		end := min(chunkBytes, len(content))
		for end > 0 && !utf8.ValidString(content[:end]) {
			end--
		}
		if end == 0 {
			break
		}
		result = append(result, prefix+content[:end])
		content = content[end:]
		prefix = fmt.Sprintf("附件 %s（续）：\n", attachment.Name)
	}
	return result
}

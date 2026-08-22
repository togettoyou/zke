package airuntime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/togettoyou/zke/pkg/server/aimodel"
	"github.com/togettoyou/zke/pkg/server/aisession"
	"github.com/togettoyou/zke/pkg/server/audit"
	"github.com/togettoyou/zke/pkg/server/rbac"
	"github.com/togettoyou/zke/pkg/shared/validation"
)

const (
	testUserID    = "11111111-1111-4111-8111-111111111111"
	testSessionID = "22222222-2222-4222-8222-222222222222"
	testProjectID = "33333333-3333-4333-8333-333333333333"
	testTenantID  = "44444444-4444-4444-8444-444444444444"
	testClusterID = "55555555-5555-4555-8555-555555555555"
)

type memorySessions struct {
	mu      sync.Mutex
	session aisession.Session
	entries []aisession.Entry
}

func (memory *memorySessions) Create(_ context.Context, input aisession.CreateInput) (aisession.Session, error) {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	memory.session = aisession.Session{ID: testSessionID, InitiatorUserID: input.InitiatorUserID,
		TenantID: input.TenantID, ProjectID: input.ProjectID, ClusterID: input.ClusterID,
		Title: input.Title, Status: aisession.StatusIdle,
		ApprovalMode: input.ApprovalMode, CreatedAt: input.Now, LastActivityAt: input.Now}
	return memory.session, nil
}

func (memory *memorySessions) Get(_ context.Context, sessionID, userID string, _ time.Time) (aisession.Session, error) {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	if memory.session.ID != sessionID || memory.session.InitiatorUserID != userID {
		return aisession.Session{}, aisession.ErrNotFound
	}
	return memory.session, nil
}

func (memory *memorySessions) Rename(
	_ context.Context, sessionID, userID, title string, _ time.Time,
) (aisession.Session, error) {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	if memory.session.ID != sessionID || memory.session.InitiatorUserID != userID {
		return aisession.Session{}, aisession.ErrNotFound
	}
	memory.session.Title = title
	return memory.session, nil
}

func (memory *memorySessions) List(context.Context, string, string, string, string, time.Time, int) ([]aisession.Session, error) {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	return []aisession.Session{memory.session}, nil
}

func (memory *memorySessions) StartTurn(_ context.Context, input aisession.StartTurnInput) (aisession.Entry, error) {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	if memory.session.Status == aisession.StatusWorking {
		return aisession.Entry{}, aisession.ErrBusy
	}
	memory.session.Status = aisession.StatusWorking
	memory.session.CurrentTurn++
	entry := aisession.Entry{Sequence: int32(len(memory.entries) + 1), Turn: memory.session.CurrentTurn,
		Kind: aisession.KindInput, OccurredAt: input.Now, Content: input.Content}
	memory.entries = append(memory.entries, entry)
	return entry, nil
}

func (memory *memorySessions) Append(_ context.Context, input aisession.AppendInput) (aisession.Entry, error) {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	if memory.session.Status != aisession.StatusWorking {
		return aisession.Entry{}, aisession.ErrIdle
	}
	entry := aisession.Entry{Sequence: int32(len(memory.entries) + 1), Turn: memory.session.CurrentTurn,
		Kind: input.Kind, OccurredAt: input.OccurredAt, Duration: input.Duration, Content: input.Content}
	memory.entries = append(memory.entries, entry)
	return entry, nil
}

func (memory *memorySessions) FinishTurn(_ context.Context, input aisession.FinishTurnInput) (aisession.Session, error) {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	if memory.session.Status != aisession.StatusWorking {
		return aisession.Session{}, aisession.ErrIdle
	}
	memory.session.Status = aisession.StatusIdle
	memory.session.LastTurnStatus = input.Status
	memory.session.LastTurnFailure = input.Failure
	return memory.session, nil
}

func (memory *memorySessions) Trajectory(_ context.Context, query aisession.TrajectoryQuery) ([]aisession.Entry, error) {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	result := make([]aisession.Entry, 0)
	for _, entry := range memory.entries {
		if entry.Sequence > query.AfterSequence {
			result = append(result, entry)
		}
	}
	return result, nil
}

func (memory *memorySessions) kinds() []aisession.Kind {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	kinds := make([]aisession.Kind, 0, len(memory.entries))
	for _, entry := range memory.entries {
		kinds = append(kinds, entry.Kind)
	}
	return kinds
}

func (memory *memorySessions) firstOf(kind aisession.Kind) (aisession.Entry, bool) {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	for _, entry := range memory.entries {
		if entry.Kind == kind {
			return entry, true
		}
	}
	return aisession.Entry{}, false
}

func idleSession(mode aisession.ApprovalMode) aisession.Session {
	now := time.Now().UTC()
	return aisession.Session{
		ID: testSessionID, InitiatorUserID: testUserID,
		TenantID: testTenantID, ProjectID: testProjectID, ClusterID: testClusterID,
		Title: "检查", Status: aisession.StatusIdle, ApprovalMode: mode,
		CreatedAt: now, LastActivityAt: now,
	}
}

type activeUsers struct{ active bool }

func (users activeUsers) IsAIUserActive(context.Context, string) (bool, error) {
	return users.active, nil
}

type allowAuthorizer struct{}

func (allowAuthorizer) AuthorizeGlobal(context.Context, string, rbac.Permission) error { return nil }
func (allowAuthorizer) AuthorizeTenant(context.Context, string, rbac.Permission, string) error {
	return nil
}
func (allowAuthorizer) AuthorizeProject(context.Context, string, rbac.Permission, string) (rbac.ResolvedScope, error) {
	return rbac.ResolvedScope{TenantID: testTenantID, ProjectID: testProjectID}, nil
}
func (allowAuthorizer) AuthorizeCluster(context.Context, string, rbac.Permission, string) (rbac.ResolvedScope, error) {
	return rbac.ResolvedScope{TenantID: testTenantID, ProjectID: testProjectID}, nil
}
func (allowAuthorizer) ResolveClusterScope(context.Context, string) (rbac.ResolvedScope, error) {
	return rbac.ResolvedScope{TenantID: testTenantID, ProjectID: testProjectID}, nil
}

type evidenceAuthorizer struct {
	allowAuthorizer
	denied rbac.Permission
}

type revocableAuthorizer struct {
	allowAuthorizer
	mu     sync.RWMutex
	denied rbac.Permission
}

func (authorizer *revocableAuthorizer) AuthorizeCluster(
	ctx context.Context, userID string, permission rbac.Permission, clusterID string,
) (rbac.ResolvedScope, error) {
	authorizer.mu.RLock()
	denied := authorizer.denied
	authorizer.mu.RUnlock()
	if permission == denied {
		return rbac.ResolvedScope{}, rbac.ErrDenied
	}
	return authorizer.allowAuthorizer.AuthorizeCluster(ctx, userID, permission, clusterID)
}

func (authorizer *revocableAuthorizer) revoke(permission rbac.Permission) {
	authorizer.mu.Lock()
	authorizer.denied = permission
	authorizer.mu.Unlock()
}

func (authorizer evidenceAuthorizer) AuthorizeCluster(
	ctx context.Context, userID string, permission rbac.Permission, clusterID string,
) (rbac.ResolvedScope, error) {
	if permission == authorizer.denied {
		return rbac.ResolvedScope{}, rbac.ErrDenied
	}
	return authorizer.allowAuthorizer.AuthorizeCluster(ctx, userID, permission, clusterID)
}

type otherProjectAuthorizer struct{ allowAuthorizer }

func (otherProjectAuthorizer) AuthorizeCluster(
	context.Context, string, rbac.Permission, string,
) (rbac.ResolvedScope, error) {
	return rbac.ResolvedScope{
		TenantID:  testTenantID,
		ProjectID: "66666666-6666-4666-8666-666666666666",
	}, nil
}

type otherTenantAuthorizer struct{ allowAuthorizer }

func (otherTenantAuthorizer) AuthorizeCluster(
	context.Context, string, rbac.Permission, string,
) (rbac.ResolvedScope, error) {
	return rbac.ResolvedScope{
		TenantID:  "77777777-7777-4777-8777-777777777777",
		ProjectID: testProjectID,
	}, nil
}

// scriptedModel answers one step at a time from a fixed script, so a test
// describes the shape of a conversation rather than a single reply.
type scriptedModel struct {
	mu        sync.Mutex
	steps     []aimodel.Completion
	index     int
	repeat    bool
	requested []aimodel.CompletionInput
	titled    int
}

func (model *scriptedModel) Get(context.Context) (aimodel.Settings, error) {
	return aimodel.Settings{
		Enabled: true, ContextWindowTokens: 64_000, MaxOutputTokens: 2_000,
	}, nil
}

func (model *scriptedModel) Complete(
	_ context.Context, input aimodel.CompletionInput,
) (aimodel.Completion, aimodel.Budget, error) {
	model.mu.Lock()
	defer model.mu.Unlock()
	// Naming a session is its own call beside the turn: it must not consume a
	// scripted step, or every test would have to script one it never asked for.
	if input.System == titlePrompt {
		model.titled++
		return aimodel.Completion{Text: "命名结果"}, aimodel.Budget{}, nil
	}
	model.requested = append(model.requested, input)
	if model.index >= len(model.steps) {
		if !model.repeat || len(model.steps) == 0 {
			return aimodel.Completion{Text: "没有更多步骤了。"}, aimodel.Budget{}, nil
		}
		return model.steps[len(model.steps)-1], aimodel.Budget{}, nil
	}
	step := model.steps[model.index]
	model.index++
	return step, aimodel.Budget{}, nil
}

func answering(text string) aimodel.Completion {
	return aimodel.Completion{Text: text, Usage: aimodel.Usage{InputTokens: 32, OutputTokens: 12}}
}

func calling(name, arguments string) aimodel.Completion {
	return aimodel.Completion{ToolCalls: []aimodel.ToolCall{
		{ID: "call_1", Name: name, Arguments: arguments},
	}}
}

// scriptedTools is a catalogue with one tool, so a test can say what the tool
// is allowed to need and observe exactly what reached it.
type scriptedTools struct {
	mu      sync.Mutex
	spec    ToolSpec
	text    string
	err     error
	result  *ToolResult
	invoked []ToolInvocation
	closed  []string
}

type blockingCloseTools struct {
	*scriptedTools
	started chan struct{}
	release chan struct{}
}

func (tools *blockingCloseTools) CloseTurn(_ context.Context, turnID string) error {
	tools.mu.Lock()
	tools.closed = append(tools.closed, turnID)
	tools.mu.Unlock()
	close(tools.started)
	<-tools.release
	return nil
}

func (tools *scriptedTools) Specs() []ToolSpec { return []ToolSpec{tools.spec} }

func (tools *scriptedTools) Invoke(
	_ context.Context, invocation ToolInvocation,
) (ToolResult, error) {
	tools.mu.Lock()
	defer tools.mu.Unlock()
	tools.invoked = append(tools.invoked, invocation)
	if tools.err != nil {
		return ToolResult{}, tools.err
	}
	if tools.result != nil {
		return *tools.result, nil
	}
	return ToolResult{Text: tools.text, Evidence: []aisession.Evidence{
		{Kind: aisession.EvidenceResource, Cluster: testClusterID},
	}}, nil
}

func (tools *scriptedTools) count() int {
	tools.mu.Lock()
	defer tools.mu.Unlock()
	return len(tools.invoked)
}

func (tools *scriptedTools) CloseTurn(_ context.Context, turnID string) error {
	tools.mu.Lock()
	defer tools.mu.Unlock()
	tools.closed = append(tools.closed, turnID)
	return nil
}

// recordingAuditor keeps what the runtime tried to record, so a test can hold
// the audit trail to the same standard as the conversation trail.
type recordingAuditor struct {
	mu     sync.Mutex
	events []audit.ClusterEventInput
}

func (auditor *recordingAuditor) RecordClusterEvent(
	_ context.Context, input audit.ClusterEventInput,
) error {
	auditor.mu.Lock()
	defer auditor.mu.Unlock()
	auditor.events = append(auditor.events, input)
	return nil
}

func (auditor *recordingAuditor) recorded() []audit.ClusterEventInput {
	auditor.mu.Lock()
	defer auditor.mu.Unlock()
	return append([]audit.ClusterEventInput(nil), auditor.events...)
}

func readOnlyTool() ToolSpec {
	return ToolSpec{
		Name: "cluster_overview", Description: "读取集群快照",
		Schema:      json.RawMessage(`{"type":"object"}`),
		Permissions: []rbac.Permission{rbac.PermissionClusterRead},
	}
}

func sensitiveTool() ToolSpec {
	spec := readOnlyTool()
	spec.Name = "get_pod_logs"
	spec.Permissions = []rbac.Permission{rbac.PermissionClusterPodLogsRead}
	spec.Sensitive = true
	return spec
}

func mutatingTool() ToolSpec {
	spec := readOnlyTool()
	spec.Name = "scale_workload"
	spec.Permissions = []rbac.Permission{rbac.PermissionClusterResourceUpdate}
	spec.Mutating = true
	return spec
}

func TestMutatingToolApprovalFollowsAllThreeModes(t *testing.T) {
	t.Parallel()
	spec := mutatingTool()
	if !requiresApproval(spec, aisession.ApprovalAsk) {
		t.Fatal("ask mode must stop before a write")
	}
	if requiresApproval(spec, aisession.ApprovalAssisted) {
		t.Fatal("assisted mode should run an ordinary write without stopping")
	}
	if requiresApproval(spec, aisession.ApprovalFull) {
		t.Fatal("full mode should not stop before an authorized write")
	}
}

func TestDynamicSensitivityOnlyUpgradesMatchingCalls(t *testing.T) {
	t.Parallel()
	spec := mutatingTool()
	spec.SensitiveWhen = func(arguments json.RawMessage) bool {
		return strings.Contains(string(arguments), `"protected"`)
	}
	if requiresApprovalFor(spec, aisession.ApprovalAssisted, json.RawMessage(`{"scope":"ordinary"}`)) {
		t.Fatal("ordinary call became sensitive")
	}
	if !requiresApprovalFor(spec, aisession.ApprovalAssisted, json.RawMessage(`{"scope":"protected"}`)) {
		t.Fatal("protected call did not become sensitive")
	}
}

func TestBackgroundTurnPersistsConclusionAndFinishes(t *testing.T) {
	t.Parallel()
	sessions := &memorySessions{session: idleSession(aisession.ApprovalAsk)}
	model := &scriptedModel{steps: []aimodel.Completion{answering("结论引用了所附证据。")}}
	runtime := New(context.Background(), sessions, model, allowAuthorizer{}, activeUsers{true}, Config{})

	if _, err := runtime.Start(context.Background(), StartInput{
		SessionID: testSessionID, UserID: testUserID, Text: "分析状态", Now: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	runtime.Wait()

	if sessions.session.LastTurnStatus != aisession.TurnSucceeded ||
		sessions.session.Status != aisession.StatusIdle {
		t.Fatalf("session after run = %#v", sessions.session)
	}
	assertKinds(t, sessions.kinds(), []aisession.Kind{
		aisession.KindInput, aisession.KindSystem, aisession.KindModel, aisession.KindConclusion,
	})
}

// A conversation opened from the composer is called "新对话" plus a clock
// reading until its first question has been asked. The first turn is what
// replaces that with something the list can be read by.
func TestFirstTurnNamesTheSession(t *testing.T) {
	t.Parallel()
	sessions := &memorySessions{session: idleSession(aisession.ApprovalAsk)}
	model := &scriptedModel{steps: []aimodel.Completion{answering("集群正常。")}}
	runtime := New(context.Background(), sessions, model, allowAuthorizer{}, activeUsers{true}, Config{})

	if _, err := runtime.Start(context.Background(), StartInput{
		SessionID: testSessionID, UserID: testUserID, Text: "看看集群", Now: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	runtime.Wait()

	sessions.mu.Lock()
	title := sessions.session.Title
	sessions.mu.Unlock()
	if title != "命名结果" {
		t.Fatalf("session title = %q, want the model's name", title)
	}
	if model.titled != 1 {
		t.Fatalf("naming calls = %d, want 1", model.titled)
	}
}

// The second turn does not rename: by then the title is either the one the
// first turn produced or one a person chose, and neither is the runtime's to
// overwrite.
func TestLaterTurnsDoNotRenameTheSession(t *testing.T) {
	t.Parallel()
	sessions := &memorySessions{session: idleSession(aisession.ApprovalAsk)}
	sessions.session.CurrentTurn = 3
	model := &scriptedModel{steps: []aimodel.Completion{answering("还是正常。")}}
	runtime := New(context.Background(), sessions, model, allowAuthorizer{}, activeUsers{true}, Config{})

	if _, err := runtime.Start(context.Background(), StartInput{
		SessionID: testSessionID, UserID: testUserID, Text: "再看一次", Now: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	runtime.Wait()

	sessions.mu.Lock()
	title := sessions.session.Title
	sessions.mu.Unlock()
	if title != "检查" || model.titled != 0 {
		t.Fatalf("session title = %q after %d naming calls", title, model.titled)
	}
}

// The loop is the point: a step that asks for a tool has to execute it and go
// back to the model with the result, rather than answering from the first reply.
func TestTurnRunsToolCallThenAsksTheModelAgain(t *testing.T) {
	t.Parallel()
	sessions := &memorySessions{session: idleSession(aisession.ApprovalAsk)}
	model := &scriptedModel{steps: []aimodel.Completion{
		calling("cluster_overview", `{}`),
		answering("集群里有两个 Pod 不就绪。"),
	}}
	tools := &scriptedTools{spec: readOnlyTool(), text: `{"pods":{"total":12,"not_ready":2}}`}
	runtime := New(context.Background(), sessions, model, allowAuthorizer{},
		activeUsers{true}, Config{Tools: tools})

	if _, err := runtime.Start(context.Background(), StartInput{
		SessionID: testSessionID, UserID: testUserID, Text: "分析状态", Now: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	runtime.Wait()

	assertKinds(t, sessions.kinds(), []aisession.Kind{
		aisession.KindInput, aisession.KindSystem, aisession.KindModel,
		aisession.KindToolCall, aisession.KindToolResult,
		aisession.KindModel, aisession.KindConclusion,
	})
	if tools.count() != 1 || tools.invoked[0].ClusterID != testClusterID ||
		tools.invoked[0].UserID != testUserID {
		t.Fatalf("tool invocation = %+v", tools.invoked)
	}
	call, _ := sessions.firstOf(aisession.KindToolCall)
	if call.Content.Tool != "cluster_overview" || call.Content.CallID != "call_1" ||
		call.Content.Authorized == nil || !*call.Content.Authorized {
		t.Fatalf("tool call entry = %+v", call)
	}
	result, _ := sessions.firstOf(aisession.KindToolResult)
	if !result.Content.Untrusted || !strings.Contains(result.Content.Text, `"total": 12`) &&
		!strings.Contains(result.Content.Text, `"total":12`) {
		t.Fatalf("tool result entry = %+v", result)
	}
	// The second request has to carry the result, or the loop is not a loop.
	if len(model.requested) != 2 {
		t.Fatalf("model calls = %d, want 2", len(model.requested))
	}
	if !containsToolMessage(model.requested[1].Messages, "call_1") {
		t.Fatalf("second request messages = %+v", model.requested[1].Messages)
	}
}

func TestToolIsNotExecutedWithoutItsOwnPermission(t *testing.T) {
	t.Parallel()
	sessions := &memorySessions{session: idleSession(aisession.ApprovalAsk)}
	model := &scriptedModel{steps: []aimodel.Completion{
		calling("cluster_overview", `{}`),
		answering("没有读取权限，只能说明限制。"),
	}}
	tools := &scriptedTools{spec: readOnlyTool(), text: "{}"}
	runtime := New(context.Background(), sessions, model,
		evidenceAuthorizer{denied: rbac.PermissionClusterRead},
		activeUsers{true}, Config{Tools: tools})

	if _, err := runtime.Start(context.Background(), StartInput{
		SessionID: testSessionID, UserID: testUserID, Text: "分析状态", Now: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	runtime.Wait()

	if tools.count() != 0 {
		t.Fatalf("tool ran without cluster.read: %+v", tools.invoked)
	}
	call, _ := sessions.firstOf(aisession.KindToolCall)
	if call.Content.Authorized == nil || *call.Content.Authorized {
		t.Fatalf("tool call entry = %+v", call)
	}
	result, _ := sessions.firstOf(aisession.KindToolResult)
	if !result.Content.Failed || !strings.Contains(result.Content.Text, string(rbac.PermissionClusterRead)) {
		t.Fatalf("tool result entry = %+v", result)
	}
}

func TestSensitiveToolWaitsForApprovalAndRunsOnceApproved(t *testing.T) {
	t.Parallel()
	sessions := &memorySessions{session: idleSession(aisession.ApprovalAsk)}
	model := &scriptedModel{steps: []aimodel.Completion{
		calling("get_pod_logs", `{"namespace":"default","pod":"web"}`),
		answering("日志里有 OOMKilled。"),
	}}
	tools := &scriptedTools{spec: sensitiveTool(), text: "OOMKilled"}
	runtime := New(context.Background(), sessions, model, allowAuthorizer{},
		activeUsers{true}, Config{Tools: tools})

	if _, err := runtime.Start(context.Background(), StartInput{
		SessionID: testSessionID, UserID: testUserID, Text: "为什么重启", Now: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		_, found := sessions.firstOf(aisession.KindApprovalRequest)
		return found
	})
	if tools.count() != 0 {
		t.Fatal("a sensitive tool ran before it was approved")
	}
	if err := runtime.Decide(context.Background(), testSessionID, testUserID,
		"call_1", aisession.DecisionApproved, time.Now().UTC()); err != nil {
		t.Fatalf("Decide() error = %v", err)
	}
	runtime.Wait()

	if tools.count() != 1 {
		t.Fatalf("approved tool ran %d times", tools.count())
	}
	decision, found := sessions.firstOf(aisession.KindApprovalDecision)
	if !found || decision.Content.Decision != aisession.DecisionApproved ||
		decision.Content.CallID != "call_1" {
		t.Fatalf("approval decision entry = %+v", decision)
	}
}

func TestApprovedWriteRechecksPermissionAfterWaiting(t *testing.T) {
	t.Parallel()
	sessions := &memorySessions{session: idleSession(aisession.ApprovalAsk)}
	model := &scriptedModel{steps: []aimodel.Completion{
		calling("scale_workload", `{"kind":"Deployment","namespace":"default","name":"web","replicas":3}`),
		answering("权限已撤销，未伸缩。"),
	}}
	tools := &scriptedTools{spec: mutatingTool(), text: "{}"}
	authorizer := &revocableAuthorizer{}
	auditor := &recordingAuditor{}
	runtime := New(context.Background(), sessions, model, authorizer,
		activeUsers{true}, Config{Tools: tools, Audit: auditor})

	if _, err := runtime.Start(context.Background(), StartInput{
		SessionID: testSessionID, UserID: testUserID, Text: "伸缩", Now: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		_, found := sessions.firstOf(aisession.KindApprovalRequest)
		return found
	})
	authorizer.revoke(rbac.PermissionClusterResourceUpdate)
	if err := runtime.Decide(context.Background(), testSessionID, testUserID,
		"call_1", aisession.DecisionApproved, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	runtime.Wait()

	if tools.count() != 0 {
		t.Fatalf("write ran after permission revocation: %+v", tools.invoked)
	}
	result, _ := sessions.firstOf(aisession.KindToolResult)
	if !result.Content.Failed || !strings.Contains(result.Content.Text, string(rbac.PermissionClusterResourceUpdate)) {
		t.Fatalf("tool result = %+v", result)
	}
	events := auditor.recorded()
	if len(events) != 1 || events[0].Result != auditResultDenied ||
		events[0].Detail["missing_permission"] != string(rbac.PermissionClusterResourceUpdate) {
		t.Fatalf("audit events = %+v", events)
	}
}

func TestDeniedApprovalKeepsTheToolFromRunningAndTellsTheModel(t *testing.T) {
	t.Parallel()
	sessions := &memorySessions{session: idleSession(aisession.ApprovalAsk)}
	model := &scriptedModel{steps: []aimodel.Completion{
		calling("get_pod_logs", `{"namespace":"default","pod":"web"}`),
		answering("用户拒绝读取日志，改用事件解释。"),
	}}
	tools := &scriptedTools{spec: sensitiveTool(), text: "OOMKilled"}
	runtime := New(context.Background(), sessions, model, allowAuthorizer{},
		activeUsers{true}, Config{Tools: tools})

	if _, err := runtime.Start(context.Background(), StartInput{
		SessionID: testSessionID, UserID: testUserID, Text: "为什么重启", Now: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		_, found := sessions.firstOf(aisession.KindApprovalRequest)
		return found
	})
	if err := runtime.Decide(context.Background(), testSessionID, testUserID,
		"call_1", aisession.DecisionDenied, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	runtime.Wait()

	if tools.count() != 0 {
		t.Fatal("a denied tool still ran")
	}
	result, _ := sessions.firstOf(aisession.KindToolResult)
	if !result.Content.Failed || !strings.Contains(result.Content.Text, "拒绝") {
		t.Fatalf("denied tool result = %+v", result)
	}
	if sessions.session.LastTurnStatus != aisession.TurnSucceeded {
		t.Fatalf("a denial is not a turn failure: %+v", sessions.session)
	}
}

// Full access changes who presses the button, not what the permissions allow.
func TestFullApprovalModeDoesNotStopForASensitiveTool(t *testing.T) {
	t.Parallel()
	sessions := &memorySessions{session: idleSession(aisession.ApprovalFull)}
	model := &scriptedModel{steps: []aimodel.Completion{
		calling("get_pod_logs", `{"namespace":"default","pod":"web"}`),
		answering("日志里有 OOMKilled。"),
	}}
	tools := &scriptedTools{spec: sensitiveTool(), text: "OOMKilled"}
	runtime := New(context.Background(), sessions, model, allowAuthorizer{},
		activeUsers{true}, Config{Tools: tools})

	if _, err := runtime.Start(context.Background(), StartInput{
		SessionID: testSessionID, UserID: testUserID, Text: "为什么重启", Now: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	runtime.Wait()

	if _, found := sessions.firstOf(aisession.KindApprovalRequest); found {
		t.Fatal("full access must not park a turn on an approval")
	}
	if tools.count() != 1 {
		t.Fatalf("tool ran %d times", tools.count())
	}
}

func TestMutatingToolReceivesAValidStableIdempotencyKey(t *testing.T) {
	t.Parallel()
	sessions := &memorySessions{session: idleSession(aisession.ApprovalFull)}
	model := &scriptedModel{steps: []aimodel.Completion{
		calling("scale_workload", `{"kind":"Deployment","namespace":"default","name":"web","replicas":3}`),
		answering("已伸缩。"),
	}}
	tools := &scriptedTools{spec: mutatingTool(), text: "{}"}
	runtime := New(context.Background(), sessions, model, allowAuthorizer{},
		activeUsers{true}, Config{Tools: tools})

	if _, err := runtime.Start(context.Background(), StartInput{
		SessionID: testSessionID, UserID: testUserID, Text: "扩到三个副本", Now: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	runtime.Wait()

	if tools.count() != 1 {
		t.Fatalf("tool ran %d times", tools.count())
	}
	key := tools.invoked[0].IdempotencyKey
	if !validation.IsIdempotencyKey(key) || !strings.HasPrefix(key, "aiops:") {
		t.Fatalf("idempotency key = %q", key)
	}
	want := toolIdempotencyKey(turnJob{
		sessionID: testSessionID, turn: 1,
	}, 1, "call_1")
	if key != want {
		t.Fatalf("idempotency key = %q, want stable %q", key, want)
	}
	wantTurnID := toolTurnID(turnJob{sessionID: testSessionID, turn: 1})
	if tools.invoked[0].TurnID != wantTurnID {
		t.Fatalf("TurnID = %q, want %q", tools.invoked[0].TurnID, wantTurnID)
	}
	if len(tools.closed) != 1 || tools.closed[0] != wantTurnID {
		t.Fatalf("closed Turns = %v, want [%s]", tools.closed, wantTurnID)
	}
}

func TestTurnRemainsRunningUntilToolResourcesAreClosed(t *testing.T) {
	sessions := &memorySessions{session: idleSession(aisession.ApprovalFull)}
	model := &scriptedModel{steps: []aimodel.Completion{answering("完成。")}}
	tools := &blockingCloseTools{
		scriptedTools: &scriptedTools{spec: mutatingTool(), text: "{}"},
		started:       make(chan struct{}),
		release:       make(chan struct{}),
	}
	runtime := New(context.Background(), sessions, model, allowAuthorizer{},
		activeUsers{true}, Config{Tools: tools})
	if _, err := runtime.Start(context.Background(), StartInput{
		SessionID: testSessionID, UserID: testUserID, Text: "检查一下",
		Now: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-tools.started:
	case <-time.After(time.Second):
		t.Fatal("Turn tool cleanup did not start")
	}
	if _, err := runtime.Start(context.Background(), StartInput{
		SessionID: testSessionID, UserID: testUserID, Text: "再检查一次",
		Now: time.Now().UTC(),
	}); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("Start() during tool cleanup error = %v, want ErrAlreadyRunning", err)
	}
	close(tools.release)
	runtime.Wait()
}

func TestMutatingApprovalNamesTheResolvedResourceTarget(t *testing.T) {
	t.Parallel()
	sessions := &memorySessions{session: idleSession(aisession.ApprovalAsk)}
	model := &scriptedModel{steps: []aimodel.Completion{
		calling("scale_workload", `{"kind":"Deployment","namespace":"team-a","name":"web","replicas":3}`),
		answering("已伸缩。"),
	}}
	spec := mutatingTool()
	spec.Target = func(json.RawMessage) *aisession.Target {
		return &aisession.Target{Namespace: "team-a", GVK: "apps/v1/Deployment", Name: "web"}
	}
	tools := &scriptedTools{spec: spec, text: "{}"}
	auditor := &recordingAuditor{}
	runtime := New(context.Background(), sessions, model, allowAuthorizer{},
		activeUsers{true}, Config{Tools: tools, Audit: auditor})

	if _, err := runtime.Start(context.Background(), StartInput{
		SessionID: testSessionID, UserID: testUserID, Text: "扩到三个副本", Now: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		_, found := sessions.firstOf(aisession.KindApprovalRequest)
		return found
	})
	approval, _ := sessions.firstOf(aisession.KindApprovalRequest)
	target := approval.Content.Target
	if target == nil || target.Cluster != testClusterID || target.Namespace != "team-a" ||
		target.GVK != "apps/v1/Deployment" || target.Name != "web" {
		t.Fatalf("approval target = %+v", target)
	}
	if err := runtime.Decide(context.Background(), testSessionID, testUserID,
		"call_1", aisession.DecisionApproved, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	runtime.Wait()
	events := auditor.recorded()
	if len(events) != 1 || events[0].Detail["mutating"] != "true" ||
		events[0].Detail["namespace"] != "team-a" ||
		events[0].Detail["gvk"] != "apps/v1/Deployment" ||
		events[0].Detail["resource_name"] != "web" {
		t.Fatalf("mutating audit event = %+v", events)
	}
}

func TestStepBudgetEndsATurnThatNeverConverges(t *testing.T) {
	t.Parallel()
	sessions := &memorySessions{session: idleSession(aisession.ApprovalFull)}
	model := &scriptedModel{
		steps:  []aimodel.Completion{calling("cluster_overview", `{}`)},
		repeat: true,
	}
	tools := &scriptedTools{spec: readOnlyTool(), text: "{}"}
	runtime := New(context.Background(), sessions, model, allowAuthorizer{},
		activeUsers{true}, Config{Tools: tools, MaxSteps: 3})

	if _, err := runtime.Start(context.Background(), StartInput{
		SessionID: testSessionID, UserID: testUserID, Text: "分析状态", Now: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	runtime.Wait()

	if sessions.session.LastTurnStatus != aisession.TurnFailed ||
		sessions.session.LastTurnFailure != aisession.FailureStepBudget {
		t.Fatalf("session after exhausted steps = %#v", sessions.session)
	}
	if len(model.requested) != 3 {
		t.Fatalf("model calls = %d, want 3", len(model.requested))
	}
}

// The same call with the same arguments cannot produce a new answer. Executing
// it a third time spends a Cluster read on nothing.
func TestRepeatedIdenticalCallStopsBeingExecuted(t *testing.T) {
	t.Parallel()
	sessions := &memorySessions{session: idleSession(aisession.ApprovalFull)}
	model := &scriptedModel{
		steps:  []aimodel.Completion{calling("cluster_overview", `{}`)},
		repeat: true,
	}
	tools := &scriptedTools{spec: readOnlyTool(), text: "{}"}
	runtime := New(context.Background(), sessions, model, allowAuthorizer{},
		activeUsers{true}, Config{Tools: tools, MaxSteps: 5})

	if _, err := runtime.Start(context.Background(), StartInput{
		SessionID: testSessionID, UserID: testUserID, Text: "分析状态", Now: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	runtime.Wait()

	if tools.count() != DefaultRepeatedCallLimit {
		t.Fatalf("tool executed %d times, want %d", tools.count(), DefaultRepeatedCallLimit)
	}
}

func TestUnknownToolIsRefusedWithTheCatalogue(t *testing.T) {
	t.Parallel()
	sessions := &memorySessions{session: idleSession(aisession.ApprovalFull)}
	model := &scriptedModel{steps: []aimodel.Completion{
		calling("delete_everything", `{}`),
		answering("没有这个工具。"),
	}}
	tools := &scriptedTools{spec: readOnlyTool(), text: "{}"}
	runtime := New(context.Background(), sessions, model, allowAuthorizer{},
		activeUsers{true}, Config{Tools: tools})

	if _, err := runtime.Start(context.Background(), StartInput{
		SessionID: testSessionID, UserID: testUserID, Text: "删掉集群", Now: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	runtime.Wait()

	if tools.count() != 0 {
		t.Fatalf("an unadvertised tool reached the catalogue: %+v", tools.invoked)
	}
	result, _ := sessions.firstOf(aisession.KindToolResult)
	if !result.Content.Failed || !strings.Contains(result.Content.Text, "cluster_overview") {
		t.Fatalf("unknown tool result = %+v", result)
	}
}

func TestReasoningIsRecordedApartFromTheAnswer(t *testing.T) {
	t.Parallel()
	sessions := &memorySessions{session: idleSession(aisession.ApprovalFull)}
	model := &scriptedModel{steps: []aimodel.Completion{{
		Text: "两个 Pod 不就绪。", Reasoning: "先看整体再看局部。",
	}}}
	runtime := New(context.Background(), sessions, model, allowAuthorizer{}, activeUsers{true}, Config{})

	if _, err := runtime.Start(context.Background(), StartInput{
		SessionID: testSessionID, UserID: testUserID, Text: "分析状态", Now: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	runtime.Wait()

	reasoning, found := sessions.firstOf(aisession.KindReasoning)
	if !found || reasoning.Content.Text != "先看整体再看局部。" || reasoning.Content.Step != 1 {
		t.Fatalf("reasoning entry = %+v", reasoning)
	}
	conclusion, _ := sessions.firstOf(aisession.KindConclusion)
	if conclusion.Content.Text != "两个 Pod 不就绪。" {
		t.Fatalf("conclusion entry = %+v", conclusion)
	}
}

// A read AIOps performs on an operator behalf has to leave the same audit trail
// a read they performed by hand would, or AIOps becomes a way around it.
func TestExecutedToolCallIsAudited(t *testing.T) {
	t.Parallel()
	sessions := &memorySessions{session: idleSession(aisession.ApprovalFull)}
	model := &scriptedModel{steps: []aimodel.Completion{
		calling("get_pod_logs", `{"namespace":"default","pod":"web"}`),
		answering("日志里有 OOMKilled。"),
	}}
	tools := &scriptedTools{spec: sensitiveTool(), text: "OOMKilled"}
	auditor := &recordingAuditor{}
	runtime := New(context.Background(), sessions, model, allowAuthorizer{},
		activeUsers{true}, Config{Tools: tools, Audit: auditor})

	if _, err := runtime.Start(context.Background(), StartInput{
		SessionID: testSessionID, UserID: testUserID, Text: "为什么重启", Now: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	runtime.Wait()

	events := auditor.recorded()
	if len(events) != 1 {
		t.Fatalf("audit events = %+v, want one", events)
	}
	event := events[0]
	if event.ActorUserID != testUserID || event.ClusterID != testClusterID ||
		event.Result != auditResultSucceeded || event.TargetName != "get_pod_logs" ||
		event.Detail["session_id"] != testSessionID {
		t.Fatalf("audit event = %+v", event)
	}
	if !strings.HasPrefix(event.RequestID, "aiops:"+testSessionID) {
		t.Fatalf("audit request id = %q", event.RequestID)
	}
}

func TestRefusedToolCallIsAuditedAsDenied(t *testing.T) {
	t.Parallel()
	sessions := &memorySessions{session: idleSession(aisession.ApprovalFull)}
	model := &scriptedModel{steps: []aimodel.Completion{
		calling("cluster_overview", `{}`),
		answering("没有读取权限。"),
	}}
	tools := &scriptedTools{spec: readOnlyTool(), text: "{}"}
	auditor := &recordingAuditor{}
	runtime := New(context.Background(), sessions, model,
		evidenceAuthorizer{denied: rbac.PermissionClusterRead},
		activeUsers{true}, Config{Tools: tools, Audit: auditor})

	if _, err := runtime.Start(context.Background(), StartInput{
		SessionID: testSessionID, UserID: testUserID, Text: "分析状态", Now: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	runtime.Wait()

	events := auditor.recorded()
	if len(events) != 1 || events[0].Result != auditResultDenied ||
		events[0].Detail["missing_permission"] != string(rbac.PermissionClusterRead) {
		t.Fatalf("audit events = %+v", events)
	}
}

func TestDynamicToolDenialIsFailedInTrajectoryAndDeniedInAudit(t *testing.T) {
	t.Parallel()
	sessions := &memorySessions{session: idleSession(aisession.ApprovalFull)}
	model := &scriptedModel{steps: []aimodel.Completion{
		calling("apply_manifest", `{"preview_id":"manifest_1"}`),
		answering("权限不足，未提交。"),
	}}
	toolResult := ToolResult{Text: "缺少 rbac_manage。", Failed: true, Denied: true}
	spec := mutatingTool()
	spec.Name = "apply_manifest"
	tools := &scriptedTools{spec: spec, result: &toolResult}
	auditor := &recordingAuditor{}
	runtime := New(context.Background(), sessions, model, allowAuthorizer{},
		activeUsers{true}, Config{Tools: tools, Audit: auditor})

	if _, err := runtime.Start(context.Background(), StartInput{
		SessionID: testSessionID, UserID: testUserID, Text: "应用清单", Now: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	runtime.Wait()
	entry, _ := sessions.firstOf(aisession.KindToolResult)
	if !entry.Content.Failed || !strings.Contains(entry.Content.Text, "rbac_manage") {
		t.Fatalf("tool result = %+v", entry)
	}
	events := auditor.recorded()
	if len(events) != 1 || events[0].Result != auditResultDenied {
		t.Fatalf("audit events = %+v", events)
	}
}

func TestCreateRequiresClusterToBelongToCurrentProject(t *testing.T) {
	t.Parallel()
	runtime := New(context.Background(), &memorySessions{}, &scriptedModel{},
		otherProjectAuthorizer{}, activeUsers{true}, Config{})

	_, err := runtime.Create(context.Background(), CreateInput{
		UserID: testUserID, TenantID: testTenantID, ProjectID: testProjectID, ClusterID: testClusterID,
		Title: "检查", Now: time.Now().UTC(),
	})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("Create() error = %v, want ErrForbidden", err)
	}
}

func TestCreateRequiresClusterToBelongToCurrentTenant(t *testing.T) {
	t.Parallel()
	runtime := New(context.Background(), &memorySessions{}, &scriptedModel{},
		otherTenantAuthorizer{}, activeUsers{true}, Config{})

	_, err := runtime.Create(context.Background(), CreateInput{
		UserID: testUserID, TenantID: testTenantID, ProjectID: testProjectID, ClusterID: testClusterID,
		Title: "检查", Now: time.Now().UTC(),
	})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("Create() error = %v, want ErrForbidden", err)
	}
}

func TestStartRejectsEvidenceFromAnotherCluster(t *testing.T) {
	t.Parallel()
	sessions := &memorySessions{session: idleSession(aisession.ApprovalAsk)}
	runtime := New(context.Background(), sessions, &scriptedModel{}, allowAuthorizer{}, activeUsers{true}, Config{})

	_, err := runtime.Start(context.Background(), StartInput{
		SessionID: testSessionID, UserID: testUserID, Text: "分析状态", Now: time.Now().UTC(),
		Evidence: []aisession.Evidence{{
			Kind:    aisession.EvidenceResource,
			Cluster: "77777777-7777-4777-8777-777777777777",
		}},
	})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("Start() error = %v, want ErrForbidden", err)
	}
	if len(sessions.entries) != 0 {
		t.Fatalf("cross-cluster evidence must not start a turn: %+v", sessions.entries)
	}
}

// Compaction shadows a head-anchored span and leaves a recent tail alone. The
// tail is what the next step reasons from, so summarizing it would replace the
// exact text of the most recent work with a description of it.
func TestCompactionPlanKeepsARecentTailVerbatim(t *testing.T) {
	t.Parallel()
	entries := []aisession.Entry{
		{Sequence: 1, Kind: aisession.KindInput, Content: aisession.Content{Text: strings.Repeat("最初目标", 200)}},
		{Sequence: 2, Kind: aisession.KindModel, Content: aisession.Content{Text: strings.Repeat("旧结论", 200)}},
		{Sequence: 3, Kind: aisession.KindInput, Content: aisession.Content{Text: strings.Repeat("最近问题", 200)}},
		{Sequence: 4, Kind: aisession.KindModel, Content: aisession.Content{Text: strings.Repeat("最近结论", 200)}},
	}

	plan, planned := planCompaction(entries, 1_000)

	if !planned {
		t.Fatal("planCompaction() found nothing to compact")
	}
	if plan.shadowed[len(plan.shadowed)-1].Sequence != 2 {
		t.Fatalf("shadowed span ends at %d, want the tail left alone",
			plan.shadowed[len(plan.shadowed)-1].Sequence)
	}
	if plan.retainedTokens < 1_000 {
		t.Fatalf("retained %d tokens, want at least the requested tail", plan.retainedTokens)
	}
}

// A tail must not begin at a tool result whose call was summarized away: an
// orphaned tool result is a request most endpoints reject outright.
func TestCompactionPlanDoesNotSplitAStepFromItsResults(t *testing.T) {
	t.Parallel()
	entries := []aisession.Entry{
		{Sequence: 1, Kind: aisession.KindInput, Content: aisession.Content{Text: strings.Repeat("目标", 400)}},
		{Sequence: 2, Kind: aisession.KindModel, Content: aisession.Content{Text: strings.Repeat("先读一次", 200), Step: 1}},
		{Sequence: 3, Kind: aisession.KindToolCall, Content: aisession.Content{
			Tool: "list_nodes", CallID: "call_1", Arguments: "{}", Step: 1,
		}},
		{Sequence: 4, Kind: aisession.KindToolResult, Content: aisession.Content{
			Tool: "list_nodes", CallID: "call_1", Text: strings.Repeat("节点", 300), Step: 1,
		}},
	}

	plan, planned := planCompaction(entries, 400)

	if !planned {
		t.Fatal("planCompaction() found nothing to compact")
	}
	if plan.shadowed[len(plan.shadowed)-1].Sequence != 1 {
		t.Fatalf("shadowed span ends at %d, want the whole step retained",
			plan.shadowed[len(plan.shadowed)-1].Sequence)
	}
}

func TestBuildMessagesUsesLatestDurableCompactionAsBaseline(t *testing.T) {
	t.Parallel()
	entries := []aisession.Entry{
		{Sequence: 1, Turn: 1, Kind: aisession.KindInput, Content: aisession.Content{Text: "已经被摘要的旧问题"}},
		{Sequence: 2, Turn: 2, Kind: aisession.KindCompaction, Content: aisession.Content{
			Text:       "保留目标和已经确认的结论",
			Evidence:   []aisession.Evidence{{Kind: aisession.EvidenceResource, Cluster: testClusterID}},
			Compaction: &aisession.Compaction{ShadowedFrom: 1, ShadowedTo: 1},
		}},
		{Sequence: 3, Turn: 2, Kind: aisession.KindModel, Content: aisession.Content{Text: "摘要后的新结论"}},
	}
	messages, evidence := buildMessages(entries)
	joined := ""
	for _, message := range messages {
		joined += message.Text + "\n"
	}
	if strings.Contains(joined, "已经被摘要的旧问题") ||
		!strings.Contains(joined, "保留目标和已经确认的结论") ||
		!strings.Contains(joined, "摘要后的新结论") {
		t.Fatalf("buildMessages() = %q", joined)
	}
	if len(evidence) != 1 || evidence[0].Cluster != testClusterID {
		t.Fatalf("buildMessages() evidence = %+v", evidence)
	}
}

// One model step that asked for several reads is one assistant turn. The trail
// interleaves each call with its result; replaying that interleaving invented
// assistant turns the model never took, and endpoints rejected the request.
func TestBuildMessagesKeepsOneStepWithParallelCallsAsOneAssistantTurn(t *testing.T) {
	t.Parallel()
	entries := []aisession.Entry{
		{Sequence: 1, Turn: 1, Kind: aisession.KindInput, Content: aisession.Content{Text: "集群怎么样"}},
		{Sequence: 2, Turn: 1, Kind: aisession.KindModel, Content: aisession.Content{Text: "我先并行读三处", Step: 1}},
		{Sequence: 3, Turn: 1, Kind: aisession.KindToolCall, Content: aisession.Content{
			Tool: "list_nodes", CallID: "call_1", Arguments: "{}", Step: 1,
		}},
		{Sequence: 4, Turn: 1, Kind: aisession.KindToolResult, Content: aisession.Content{
			Tool: "list_nodes", CallID: "call_1", Text: "节点", Step: 1,
		}},
		{Sequence: 5, Turn: 1, Kind: aisession.KindToolCall, Content: aisession.Content{
			Tool: "list_resources", CallID: "call_2", Arguments: "{}", Step: 1,
		}},
		{Sequence: 6, Turn: 1, Kind: aisession.KindToolResult, Content: aisession.Content{
			Tool: "list_resources", CallID: "call_2", Text: "工作负载", Step: 1,
		}},
		{Sequence: 7, Turn: 1, Kind: aisession.KindToolCall, Content: aisession.Content{
			Tool: "list_resources", CallID: "call_3", Arguments: "{}", Step: 1,
		}},
		{Sequence: 8, Turn: 1, Kind: aisession.KindToolResult, Content: aisession.Content{
			Tool: "list_resources", CallID: "call_3", Text: "命名空间", Step: 1,
		}},
	}
	messages, _ := buildMessages(entries)
	if len(messages) != 5 {
		t.Fatalf("buildMessages() = %d messages, want 5: %+v", len(messages), messages)
	}
	assistants := 0
	for _, message := range messages {
		if message.Role == aimodel.RoleAssistant {
			assistants++
		}
	}
	if assistants != 1 {
		t.Fatalf("buildMessages() produced %d assistant turns, want 1: %+v", assistants, messages)
	}
	if len(messages[1].ToolCalls) != 3 {
		t.Fatalf("assistant turn carries %d calls, want 3: %+v", len(messages[1].ToolCalls), messages[1])
	}
	for index, callID := range []string{"call_1", "call_2", "call_3"} {
		result := messages[2+index]
		if result.Role != aimodel.RoleTool || result.ToolCallID != callID {
			t.Fatalf("message %d = %+v, want the result of %s", 2+index, result, callID)
		}
	}
}

// The row of links under an answer is what this turn read, once each — not
// everything the conversation has ever touched.
func TestTurnEvidenceIsDistinctAndScopedToItsOwnTurn(t *testing.T) {
	t.Parallel()
	pod := aisession.Evidence{
		Kind: aisession.EvidenceResource, Cluster: testClusterID,
		Namespace: "default", GVK: "v1/Pod", Name: "web-0",
	}
	log := aisession.Evidence{
		Kind: aisession.EvidenceLog, Cluster: testClusterID,
		Namespace: "default", GVK: "v1/Pod", Name: "web-0", Container: "app",
		From: time.Now().Add(-time.Hour), To: time.Now(),
	}
	entries := []aisession.Entry{
		{Sequence: 1, Turn: 1, Kind: aisession.KindInput, Content: aisession.Content{Text: "上一轮"}},
		{Sequence: 2, Turn: 1, Kind: aisession.KindToolResult, Content: aisession.Content{
			Tool: "get_resource", CallID: "call_0", Evidence: []aisession.Evidence{
				{Kind: aisession.EvidenceResource, Cluster: testClusterID, GVK: "v1/Node", Name: "node-1"},
			},
		}},
		{Sequence: 3, Turn: 2, Kind: aisession.KindInput, Content: aisession.Content{Text: "这一轮"}},
		{Sequence: 4, Turn: 2, Kind: aisession.KindToolResult, Content: aisession.Content{
			Tool: "get_resource", CallID: "call_1", Evidence: []aisession.Evidence{pod},
		}},
		{Sequence: 5, Turn: 2, Kind: aisession.KindToolResult, Content: aisession.Content{
			Tool: "get_resource", CallID: "call_2", Evidence: []aisession.Evidence{pod},
		}},
		{Sequence: 6, Turn: 2, Kind: aisession.KindToolResult, Content: aisession.Content{
			Tool: "get_pod_logs", CallID: "call_3", Evidence: []aisession.Evidence{log},
		}},
	}
	evidence := turnEvidence(entries, 2)
	if len(evidence) != 2 {
		t.Fatalf("turnEvidence() = %d references, want 2: %+v", len(evidence), evidence)
	}
	if evidence[0].Name != "web-0" || evidence[0].Kind != aisession.EvidenceResource {
		t.Fatalf("first reference = %+v", evidence[0])
	}
	if evidence[1].Kind != aisession.EvidenceLog || evidence[1].Container != "app" {
		t.Fatalf("second reference = %+v", evidence[1])
	}
	// A turn that read nothing cites nothing: "谢谢你" does not rest on ten
	// objects somebody looked at half an hour ago.
	if quiet := turnEvidence(entries, 3); len(quiet) != 0 {
		t.Fatalf("a turn with no reads cited %+v", quiet)
	}
}

// A tool result whose call is no longer readable would be an orphan most
// endpoints reject outright.
func TestBuildMessagesDropsToolResultsWithoutTheirCall(t *testing.T) {
	t.Parallel()
	entries := []aisession.Entry{
		{Sequence: 1, Turn: 1, Kind: aisession.KindInput, Content: aisession.Content{Text: "问题"}},
		{Sequence: 2, Turn: 1, Kind: aisession.KindToolResult, Content: aisession.Content{
			Tool: "cluster_overview", CallID: "call_gone", Text: "孤立的工具结果",
		}},
	}
	messages, _ := buildMessages(entries)
	for _, message := range messages {
		if message.Role == aimodel.RoleTool {
			t.Fatalf("orphaned tool result reached the model: %+v", messages)
		}
	}
}

func TestStartDoesNotPersistASecondTurnWhileTheJobStillExists(t *testing.T) {
	t.Parallel()
	sessions := &memorySessions{session: idleSession(aisession.ApprovalAsk)}
	runtime := New(context.Background(), sessions, &scriptedModel{}, allowAuthorizer{}, activeUsers{true}, Config{})
	runtime.jobs[testSessionID] = func() {}

	_, err := runtime.Start(context.Background(), StartInput{
		SessionID: testSessionID, UserID: testUserID, Text: "第二个问题", Now: time.Now().UTC(),
	})
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("Start() error = %v, want ErrAlreadyRunning", err)
	}
	if sessions.session.Status != aisession.StatusIdle || len(sessions.entries) != 0 {
		t.Fatalf("a rejected turn must not be persisted: session=%+v entries=%+v", sessions.session, sessions.entries)
	}
}

func TestHistoryRechecksEvidenceBeforeReturningItToTheModel(t *testing.T) {
	t.Parallel()
	sessions := &memorySessions{session: idleSession(aisession.ApprovalAsk), entries: []aisession.Entry{{
		Sequence: 1, Turn: 1, Kind: aisession.KindContext,
		Content: aisession.Content{Text: "敏感指标正文", Evidence: []aisession.Evidence{{
			Kind: aisession.EvidenceMetric, Cluster: testClusterID, Query: "node_cpu",
		}}},
	}}}
	runtime := New(context.Background(), sessions, &scriptedModel{}, evidenceAuthorizer{
		denied: rbac.PermissionClusterMetricsRead,
	}, activeUsers{true}, Config{})

	history, err := runtime.loadHistory(context.Background(), testSessionID, testUserID)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].Content.Text != "当前权限已无法读取这项集群证据" ||
		len(history[0].Content.Evidence) != 0 {
		t.Fatalf("revoked evidence was not redacted: %+v", history)
	}
}

func TestTrajectoryAddsAuthoritativeEvidenceNavigationScope(t *testing.T) {
	t.Parallel()
	sessions := &memorySessions{session: idleSession(aisession.ApprovalAsk), entries: []aisession.Entry{{
		Sequence: 1, Turn: 1, Kind: aisession.KindConclusion,
		Content: aisession.Content{Evidence: []aisession.Evidence{{
			Kind: aisession.EvidenceResource, Cluster: testClusterID,
		}}},
	}}}
	runtime := New(context.Background(), sessions, &scriptedModel{}, allowAuthorizer{}, activeUsers{true}, Config{})

	entries, err := runtime.Trajectory(context.Background(), aisession.TrajectoryQuery{
		SessionID: testSessionID, InitiatorUserID: testUserID, Now: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	evidence := entries[0].Content.Evidence[0]
	if evidence.TenantID == "" || evidence.ProjectID != testProjectID {
		t.Fatalf("evidence navigation scope = %+v", evidence)
	}
}

func TestDecideRefusesACallNobodyIsWaitingOn(t *testing.T) {
	t.Parallel()
	sessions := &memorySessions{session: idleSession(aisession.ApprovalAsk)}
	runtime := New(context.Background(), sessions, &scriptedModel{}, allowAuthorizer{}, activeUsers{true}, Config{})

	err := runtime.Decide(context.Background(), testSessionID, testUserID,
		"call_1", aisession.DecisionApproved, time.Now().UTC())
	if !errors.Is(err, ErrNoPendingApproval) {
		t.Fatalf("Decide() error = %v, want ErrNoPendingApproval", err)
	}
}

// settingsModel answers only the settings question, which is all Enabled asks.
type settingsModel struct {
	settings aimodel.Settings
}

func (model settingsModel) Get(context.Context) (aimodel.Settings, error) {
	return model.settings, nil
}

func (settingsModel) Complete(
	context.Context, aimodel.CompletionInput,
) (aimodel.Completion, aimodel.Budget, error) {
	return aimodel.Completion{}, aimodel.Budget{}, nil
}

// A switched-on deployment with no endpoint is not available: the Console hides
// the application on this answer, and reporting availability there would put an
// icon on the desktop whose every turn the runtime refuses.
func TestRuntimeEnabledRequiresEndpoint(t *testing.T) {
	cases := []struct {
		name     string
		settings aimodel.Settings
		want     bool
	}{
		{"switched off", aimodel.Settings{BaseURL: "https://model.internal/v1", Model: "m"}, false},
		{"no endpoint", aimodel.Settings{Enabled: true, Model: "m"}, false},
		{"no model name", aimodel.Settings{Enabled: true, BaseURL: "https://model.internal/v1"}, false},
		{"configured", aimodel.Settings{
			Enabled: true, BaseURL: "https://model.internal/v1", Model: "m",
		}, true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			runtime := New(context.Background(), &memorySessions{},
				settingsModel{settings: testCase.settings}, allowAuthorizer{}, activeUsers{true}, Config{})
			enabled, err := runtime.Enabled(context.Background())
			if err != nil {
				t.Fatalf("Enabled() error = %v", err)
			}
			if enabled != testCase.want {
				t.Fatalf("Enabled() = %v, want %v", enabled, testCase.want)
			}
		})
	}
}

func assertKinds(t *testing.T, got, want []aisession.Kind) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("entry kinds = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("entry kinds = %v, want %v", got, want)
		}
	}
}

func containsToolMessage(messages []aimodel.Message, callID string) bool {
	for _, message := range messages {
		if message.Role == aimodel.RoleTool && message.ToolCallID == callID {
			return true
		}
	}
	return false
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition was not reached in time")
}

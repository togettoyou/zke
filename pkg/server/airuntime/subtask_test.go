package airuntime

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/togettoyou/zke/pkg/server/aimodel"
	"github.com/togettoyou/zke/pkg/server/aisession"
	"github.com/togettoyou/zke/pkg/server/rbac"
)

// delegatingModel answers the main line from a script and every branch from one
// function of its goal.
//
// Telling them apart by the system instruction rather than by call order is the
// point: branches run concurrently, so a test that assumed an order would be
// testing the scheduler. It also asserts the boundary the design promises — a
// branch is handed its own instruction and its own brief, never the
// conversation.
type delegatingModel struct {
	mu     sync.Mutex
	steps  []aimodel.Completion
	index  int
	branch func(goal string) (aimodel.Completion, error)
	// briefs is what each branch was actually given, in arrival order.
	briefs []string
	// branchTools is the catalogue each branch was offered.
	branchTools [][]string
	// requests is every main-line request, so a test can hold the projection
	// the endpoint was actually sent to the shape endpoints require.
	requests [][]aimodel.Message
	// branchRequests is the same for every branch request.
	branchRequests [][]aimodel.Message
}

func (model *delegatingModel) Get(context.Context) (aimodel.Settings, error) {
	return aimodel.Settings{
		Enabled: true, ContextWindowTokens: 64_000, MaxOutputTokens: 2_000,
	}, nil
}

func (model *delegatingModel) Complete(
	_ context.Context, input aimodel.CompletionInput,
) (aimodel.Completion, aimodel.Budget, error) {
	if input.System == titlePrompt {
		return aimodel.Completion{Text: "命名结果"}, aimodel.Budget{}, nil
	}
	if strings.HasPrefix(input.System, "你是 ZKE AIOps 的一个取证子任务") {
		brief := input.Messages[0].Text
		names := make([]string, 0, len(input.Tools))
		for _, tool := range input.Tools {
			names = append(names, tool.Name)
		}
		model.mu.Lock()
		model.briefs = append(model.briefs, brief)
		model.branchTools = append(model.branchTools, names)
		model.branchRequests = append(model.branchRequests, slices.Clone(input.Messages))
		model.mu.Unlock()
		return callBranch(model.branch, goalOf(brief))
	}
	model.mu.Lock()
	defer model.mu.Unlock()
	model.requests = append(model.requests, slices.Clone(input.Messages))
	if model.index >= len(model.steps) {
		return aimodel.Completion{Text: "没有更多步骤了。"}, aimodel.Budget{}, nil
	}
	step := model.steps[model.index]
	model.index++
	return step, aimodel.Budget{}, nil
}

func (model *delegatingModel) mainLineRequests() [][]aimodel.Message {
	model.mu.Lock()
	defer model.mu.Unlock()
	return slices.Clone(model.requests)
}

func (model *delegatingModel) branchLineRequests() [][]aimodel.Message {
	model.mu.Lock()
	defer model.mu.Unlock()
	return slices.Clone(model.branchRequests)
}

func callBranch(
	branch func(string) (aimodel.Completion, error), goal string,
) (aimodel.Completion, aimodel.Budget, error) {
	completion, err := branch(goal)
	return completion, aimodel.Budget{}, err
}

func goalOf(brief string) string {
	line := brief
	if index := strings.IndexAny(line, "\r\n"); index >= 0 {
		line = line[:index]
	}
	return strings.TrimPrefix(line, "任务目标：")
}

func (model *delegatingModel) collected() ([]string, [][]string) {
	model.mu.Lock()
	defer model.mu.Unlock()
	return slices.Clone(model.briefs), slices.Clone(model.branchTools)
}

func delegating(goals ...string) aimodel.Completion {
	branches := make([]map[string]string, 0, len(goals))
	for _, goal := range goals {
		branches = append(branches, map[string]string{"goal": goal})
	}
	arguments, err := json.Marshal(map[string]any{"subtasks": branches})
	if err != nil {
		panic(err)
	}
	return calling(toolRunSubtasks, string(arguments))
}

func readOnlyTools() *scriptedTools {
	return &scriptedTools{
		spec: ToolSpec{
			Name: "list_resources", Description: "列出对象。",
			Schema:      json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
			Permissions: []rbac.Permission{rbac.PermissionClusterRead},
		},
		text: "两个 Pod 处于 CrashLoopBackOff。",
	}
}

func subtaskRuntime(
	sessions *memorySessions, model ModelService, tools ToolSet, parallel int,
) *Runtime {
	return New(context.Background(), sessions, model, allowAuthorizer{}, activeUsers{true}, Config{
		Tools:   tools,
		Subtask: SubtaskConfig{MaxParallel: parallel, MaxSteps: 3, MaxToolCalls: 4},
	})
}

// Delegation has to buy the main line one answer, not three conversations it
// then has to read. The branches' own steps stay in the trail — that is what
// makes the run reviewable — but they are stamped, so nothing on the main line
// is confused for something a branch said.
func TestSubtasksFoldIntoOneResultOnTheMainLine(t *testing.T) {
	t.Parallel()
	sessions := &memorySessions{session: idleSession(aisession.ApprovalFull)}
	model := &delegatingModel{
		steps: []aimodel.Completion{
			delegating("确认副本为什么不就绪", "确认最近的 Event"),
			answering("两个分支一致指向镜像拉取失败。"),
		},
		branch: func(goal string) (aimodel.Completion, error) {
			return answering("分支结论：" + goal), nil
		},
	}
	runtime := subtaskRuntime(sessions, model, readOnlyTools(), 3)

	if _, err := runtime.Start(context.Background(), StartInput{
		SessionID: testSessionID, UserID: testUserID, Text: "为什么不可用", Now: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	runtime.Wait()

	result := mainLineResult(sessions, toolRunSubtasks)
	if result == nil {
		t.Fatalf("no folded result on the main line: %v", entryKinds(sessions))
	}
	for _, goal := range []string{"确认副本为什么不就绪", "确认最近的 Event"} {
		if !strings.Contains(result.Content.Text, "分支结论："+goal) {
			t.Fatalf("folded result is missing branch %q:\n%s", goal, result.Content.Text)
		}
	}
	if result.Content.Subtask != nil {
		t.Fatal("the delegating call itself must stay on the main line")
	}
	if !result.Content.Untrusted {
		t.Fatal("branch conclusions are derived from cluster content and stay untrusted")
	}
	if branches := stampedBranches(sessions); len(branches) != 2 {
		t.Fatalf("branch identities in the trail = %v, want two", branches)
	}
	if !hasEntry(sessions, aisession.KindConclusion, "两个分支一致指向镜像拉取失败。") {
		t.Fatalf("turn did not conclude: %v", entryKinds(sessions))
	}
}

// A branch is a read-only investigation that cannot delegate again. Both are
// enforced by what it is offered rather than by a counter: a tool it cannot see
// is a tool it cannot call, however the model reasons about it.
func TestSubtaskIsOfferedOnlyReadOnlyToolsAndCannotRecurse(t *testing.T) {
	t.Parallel()
	sessions := &memorySessions{session: idleSession(aisession.ApprovalFull)}
	writing := readOnlyTools()
	writing.spec.Name = "scale_workload"
	writing.spec.Mutating = true
	model := &delegatingModel{
		steps: []aimodel.Completion{
			delegating("确认副本数"),
			answering("已汇总。"),
		},
		branch: func(goal string) (aimodel.Completion, error) {
			return answering("分支结论：" + goal), nil
		},
	}
	runtime := subtaskRuntime(sessions, model, writing, 3)

	// The only tool this deployment has is a write, so delegation has nothing
	// to offer a branch and must not be advertised at all.
	if slices.Contains(specNames(runtime.ToolCatalogue()), toolRunSubtasks) {
		t.Fatal("delegation advertised with no read-only tool to delegate")
	}

	readable := readOnlyTools()
	runtime = subtaskRuntime(sessions, model, readable, 3)
	if _, err := runtime.Start(context.Background(), StartInput{
		SessionID: testSessionID, UserID: testUserID, Text: "看一下", Now: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	runtime.Wait()

	_, offered := model.collected()
	if len(offered) != 1 {
		t.Fatalf("branches started = %d, want one", len(offered))
	}
	if slices.Contains(offered[0], toolRunSubtasks) {
		t.Fatalf("a branch was offered delegation: %v", offered[0])
	}
	if !slices.Contains(offered[0], readable.spec.Name) {
		t.Fatalf("a branch was not offered the read tool: %v", offered[0])
	}
}

// A branch sees its goal and nothing else. The conversation that spawned it is
// deliberately out of reach: delegation that copied the whole session would buy
// latency at three times the context, and a branch reading the user's words
// would answer the question instead of the one it was given.
func TestSubtaskReceivesOnlyItsBrief(t *testing.T) {
	t.Parallel()
	sessions := &memorySessions{session: idleSession(aisession.ApprovalFull)}
	arguments, err := json.Marshal(map[string]any{"subtasks": []map[string]string{
		{"goal": "确认 ns/web 的 Event", "context": "用户说十分钟前开始报错"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	model := &delegatingModel{
		steps: []aimodel.Completion{
			calling(toolRunSubtasks, string(arguments)),
			answering("已汇总。"),
		},
		branch: func(goal string) (aimodel.Completion, error) {
			return answering("分支结论：" + goal), nil
		},
	}
	runtime := subtaskRuntime(sessions, model, readOnlyTools(), 3)

	if _, err := runtime.Start(context.Background(), StartInput{
		SessionID: testSessionID, UserID: testUserID,
		Text: "线上出问题了，用户投诉打不开页面", Now: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	runtime.Wait()

	briefs, _ := model.collected()
	if len(briefs) != 1 {
		t.Fatalf("branches started = %d, want one", len(briefs))
	}
	if !strings.Contains(briefs[0], "确认 ns/web 的 Event") {
		t.Fatalf("brief is missing the goal:\n%s", briefs[0])
	}
	if !strings.Contains(briefs[0], "用户说十分钟前开始报错") {
		t.Fatalf("brief is missing what the main line passed along:\n%s", briefs[0])
	}
	if strings.Contains(briefs[0], "用户投诉打不开页面") {
		t.Fatalf("the conversation leaked into a branch:\n%s", briefs[0])
	}
}

// A branch that could not finish is a fact the main line has to reason about,
// not the end of the turn. Reporting it as a classification rather than as a
// missing answer is what stops the model from filling the gap with a guess.
func TestFailedBranchIsReportedWithoutEndingTheTurn(t *testing.T) {
	t.Parallel()
	sessions := &memorySessions{session: idleSession(aisession.ApprovalFull)}
	model := &delegatingModel{
		steps: []aimodel.Completion{
			delegating("查 Event", "查指标"),
			answering("只有一个分支有结论。"),
		},
		branch: func(goal string) (aimodel.Completion, error) {
			if goal == "查指标" {
				return aimodel.Completion{}, &aimodel.CallError{
					Kind: aimodel.CallAuth, Status: 401,
				}
			}
			return answering("分支结论：" + goal), nil
		},
	}
	runtime := subtaskRuntime(sessions, model, readOnlyTools(), 3)

	if _, err := runtime.Start(context.Background(), StartInput{
		SessionID: testSessionID, UserID: testUserID, Text: "查一下", Now: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	runtime.Wait()

	result := mainLineResult(sessions, toolRunSubtasks)
	if result == nil {
		t.Fatalf("no folded result: %v", entryKinds(sessions))
	}
	if !strings.Contains(result.Content.Text, aisession.FailureModelRejected) {
		t.Fatalf("the failed branch is not classified:\n%s", result.Content.Text)
	}
	if !strings.Contains(result.Content.Text, "分支结论：查 Event") {
		t.Fatalf("the surviving branch was lost:\n%s", result.Content.Text)
	}
	if result.Content.Failed {
		t.Fatal("one branch of two failing is not a failed delegation")
	}
	if session := sessionOf(sessions); session.LastTurnStatus != aisession.TurnSucceeded {
		t.Fatalf("turn status = %q, a branch failure must not end the turn", session.LastTurnStatus)
	}
}

// The bound is refused rather than silently trimmed. A model told it asked for
// too much can merge its branches; one whose fourth question was dropped
// without a word concludes from an investigation it does not know is partial.
func TestDelegationBeyondTheBoundIsRefused(t *testing.T) {
	t.Parallel()
	sessions := &memorySessions{session: idleSession(aisession.ApprovalFull)}
	model := &delegatingModel{
		steps: []aimodel.Completion{
			delegating("一", "二", "三"),
			answering("改为自己查。"),
		},
		branch: func(goal string) (aimodel.Completion, error) {
			return answering("分支结论：" + goal), nil
		},
	}
	runtime := subtaskRuntime(sessions, model, readOnlyTools(), 2)

	if _, err := runtime.Start(context.Background(), StartInput{
		SessionID: testSessionID, UserID: testUserID, Text: "查一下", Now: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	runtime.Wait()

	result := mainLineResult(sessions, toolRunSubtasks)
	if result == nil || !result.Content.Failed {
		t.Fatalf("over-wide delegation was not refused: %+v", result)
	}
	if briefs, _ := model.collected(); len(briefs) != 0 {
		t.Fatalf("branches started = %d, want none", len(briefs))
	}
}

// A deployment that switches delegation off must not be offered it. An
// advertised tool that always refuses costs a step every time the model
// believes in it.
func TestDelegationIsAbsentWhenDisabled(t *testing.T) {
	t.Parallel()
	sessions := &memorySessions{session: idleSession(aisession.ApprovalFull)}
	runtime := subtaskRuntime(sessions, &delegatingModel{}, readOnlyTools(), 0)
	if slices.Contains(specNames(runtime.ToolCatalogue()), toolRunSubtasks) {
		t.Fatal("delegation advertised in a deployment that switched it off")
	}
}

// mainLineResult is the result of one tool call the turn itself made.
func mainLineResult(sessions *memorySessions, tool string) *aisession.Entry {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	for index := range sessions.entries {
		entry := &sessions.entries[index]
		if entry.Kind == aisession.KindToolResult && entry.Content.Tool == tool &&
			entry.Content.Subtask == nil {
			return entry
		}
	}
	return nil
}

// stampedBranches is every branch identity the trail carries.
func stampedBranches(sessions *memorySessions) []string {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	seen := make(map[string]struct{})
	branches := make([]string, 0, 2)
	for _, entry := range sessions.entries {
		if entry.Content.Subtask == nil {
			continue
		}
		if _, duplicate := seen[entry.Content.Subtask.ID]; duplicate {
			continue
		}
		seen[entry.Content.Subtask.ID] = struct{}{}
		branches = append(branches, entry.Content.Subtask.ID)
	}
	return branches
}

func sessionOf(sessions *memorySessions) aisession.Session {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	return sessions.session
}

// The goal is on the entry that opens a branch and nowhere else. Repeating a
// few hundred model-written characters on every row would put the same text
// into the trail a dozen times for a reader who already knows which branch they
// are looking at.
func TestBranchGoalIsRecordedOnceAtItsOpening(t *testing.T) {
	t.Parallel()
	sessions := &memorySessions{session: idleSession(aisession.ApprovalFull)}
	model := &delegatingModel{
		steps: []aimodel.Completion{
			delegating("确认副本为什么不就绪"),
			answering("已汇总。"),
		},
		branch: func(goal string) (aimodel.Completion, error) {
			return calling("list_resources", "{}"), nil
		},
	}
	runtime := subtaskRuntime(sessions, model, readOnlyTools(), 3)

	if _, err := runtime.Start(context.Background(), StartInput{
		SessionID: testSessionID, UserID: testUserID, Text: "查一下", Now: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	runtime.Wait()

	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	goals := 0
	stamped := 0
	for _, entry := range sessions.entries {
		if entry.Content.Subtask == nil {
			continue
		}
		stamped++
		if entry.Content.Subtask.Goal != "" {
			goals++
		}
	}
	if stamped < 2 {
		t.Fatalf("branch wrote %d stamped entries, want several", stamped)
	}
	if goals != 1 {
		t.Fatalf("goal appears on %d of %d branch entries, want exactly one", goals, stamped)
	}
}

// The branches' own steps must not reach the main line's model request.
//
// This is the shape endpoints actually enforce: a tool result has to follow the
// assistant message that requested it, with nothing in between. Branch entries
// live in the same trail as the turn, so a projection that did not separate the
// two lines put a branch's assistant message between the delegating call and
// its result — and every endpoint refused the whole request, which reached the
// operator as `model_rejected` one step after a delegation that had worked.
func TestBranchStepsStayOutOfTheMainLineRequest(t *testing.T) {
	t.Parallel()
	sessions := &memorySessions{session: idleSession(aisession.ApprovalFull)}
	model := &delegatingModel{
		steps: []aimodel.Completion{
			delegating("查 Event", "查副本"),
			answering("已汇总两个分支。"),
		},
		branch: func(goal string) (aimodel.Completion, error) {
			// A branch that reads something, so it produces its own assistant
			// message and its own tool result to leak.
			return calling("list_resources", "{}"), nil
		},
	}
	runtime := subtaskRuntime(sessions, model, readOnlyTools(), 3)

	if _, err := runtime.Start(context.Background(), StartInput{
		SessionID: testSessionID, UserID: testUserID, Text: "查一下", Now: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	runtime.Wait()

	requests := model.mainLineRequests()
	if len(requests) < 2 {
		t.Fatalf("main line made %d requests, want at least two", len(requests))
	}
	last := requests[len(requests)-1]
	for index, message := range last {
		for _, call := range message.ToolCalls {
			if call.Name != toolRunSubtasks {
				t.Fatalf("a branch's call reached the main line request: %+v", last)
			}
			// The result of a call has to be the very next message. Anything
			// between them is what the endpoint refuses.
			next := last[index+1]
			if next.Role != aimodel.RoleTool || next.ToolCallID != call.ID {
				t.Fatalf("message after the delegating call is %+v, want its result", next)
			}
		}
	}
	if slices.ContainsFunc(last, func(message aimodel.Message) bool {
		return message.Role == aimodel.RoleTool && message.ToolName == "list_resources"
	}) {
		t.Fatalf("a branch's tool result reached the main line request: %+v", last)
	}
}

// A branch's usage prices a different request: its own brief, its own narrowed
// tool schemas. Anchoring the conversation on one would report the turn as
// whatever the smallest branch happened to cost, and the first thing to break
// would be compacting before the endpoint refuses the request.
func TestBranchStepsDoNotAnchorTheMainLineMeasurement(t *testing.T) {
	t.Parallel()
	main := aisession.Entry{
		Sequence: 1, Turn: 1, Kind: aisession.KindModel,
		Content: aisession.Content{
			Text: "主线步骤", Tokens: &aisession.Tokens{Input: 9_000, Output: 20},
		},
	}
	branch := aisession.Entry{
		Sequence: 2, Turn: 1, Kind: aisession.KindModel,
		Content: aisession.Content{
			Text:    "分支步骤",
			Tokens:  &aisession.Tokens{Input: 40, Output: 5},
			Subtask: &aisession.Subtask{ID: "call_1-1", CallID: "call_1", Index: 1},
		},
	}

	pressure := measure([]aisession.Entry{main, branch}, "系统指令", nil)

	if !pressure.Measured {
		t.Fatal("measurement lost its anchor to the branch entry")
	}
	if pressure.TotalTokens < 9_000 {
		t.Fatalf("total = %d, want it anchored on the main line's 9000", pressure.TotalTokens)
	}
}

// Two branches are two conversations, so their models both number the first
// call `call_1`. That identifier ties an approval to the call parked on it and
// a result to the call that produced it, so branches must not share one — a
// person's approval would otherwise be delivered to the wrong branch.
func TestBranchCallIdentifiersDoNotCollide(t *testing.T) {
	t.Parallel()
	sessions := &memorySessions{session: idleSession(aisession.ApprovalFull)}
	model := &delegatingModel{
		steps: []aimodel.Completion{
			delegating("查 Event", "查副本"),
			answering("已汇总。"),
		},
		branch: func(goal string) (aimodel.Completion, error) {
			// Both branches hand back the identifier every endpoint hands back
			// first.
			return calling("list_resources", "{}"), nil
		},
	}
	runtime := subtaskRuntime(sessions, model, readOnlyTools(), 3)

	if _, err := runtime.Start(context.Background(), StartInput{
		SessionID: testSessionID, UserID: testUserID, Text: "查一下", Now: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	runtime.Wait()

	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	owners := make(map[string]string)
	for _, entry := range sessions.entries {
		if entry.Kind != aisession.KindToolCall {
			continue
		}
		callID := entry.Content.CallID
		branch := ""
		if entry.Content.Subtask != nil {
			branch = entry.Content.Subtask.ID
		}
		if owner, seen := owners[callID]; seen && owner != branch {
			t.Fatalf("call id %q is shared by %q and %q", callID, owner, branch)
		}
		owners[callID] = branch
	}
	if len(owners) != 3 {
		t.Fatalf("distinct call ids = %d, want the delegation plus one per branch: %v",
			len(owners), owners)
	}
}

// The endpoint gets back the identifier it issued, not ZKE's.
//
// The trail qualifies a branch's calls with the branch, because that is what an
// approval is answered by and what pairs a result to its call. The endpoint's
// rules are different and not negotiable: the value must be the one it issued,
// and providers bound its length and its character set. Sending the qualified
// form made every branch's second request — the first one carrying a call in
// its history — fail as `model_rejected`, after the branch had already read the
// Cluster.
func TestBranchSendsBackTheEndpointsOwnCallIdentifier(t *testing.T) {
	t.Parallel()
	const issued = "call_00_endpointIssuedIdentifier"
	sessions := &memorySessions{session: idleSession(aisession.ApprovalFull)}
	var branchMu sync.Mutex
	steps := make(map[string]int)
	model := &delegatingModel{
		steps: []aimodel.Completion{
			delegating("查节点"),
			answering("已汇总。"),
		},
		branch: func(goal string) (aimodel.Completion, error) {
			branchMu.Lock()
			steps[goal]++
			step := steps[goal]
			branchMu.Unlock()
			if step == 1 {
				return aimodel.Completion{ToolCalls: []aimodel.ToolCall{
					{ID: issued, Name: "list_resources", Arguments: "{}"},
				}}, nil
			}
			return answering("分支结论：" + goal), nil
		},
	}
	runtime := subtaskRuntime(sessions, model, readOnlyTools(), 3)

	if _, err := runtime.Start(context.Background(), StartInput{
		SessionID: testSessionID, UserID: testUserID, Text: "查一下", Now: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	runtime.Wait()

	requests := model.branchLineRequests()
	if len(requests) < 2 {
		t.Fatalf("branch made %d requests, want a second one carrying its call", len(requests))
	}
	last := requests[len(requests)-1]
	calls := 0
	for _, message := range last {
		for _, call := range message.ToolCalls {
			calls++
			if call.ID != issued {
				t.Fatalf("branch sent back call id %q, want the endpoint's own %q", call.ID, issued)
			}
		}
		if message.Role == aimodel.RoleTool && message.ToolCallID != issued {
			t.Fatalf("branch sent back result for %q, want %q", message.ToolCallID, issued)
		}
	}
	if calls != 1 {
		t.Fatalf("branch projection carried %d calls, want the one it made", calls)
	}
	// And the trail still separates the branch's call from anything else in the
	// session, which is the reason the qualifier exists at all.
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	for _, entry := range sessions.entries {
		if entry.Kind != aisession.KindToolCall || entry.Content.Subtask == nil {
			continue
		}
		if entry.Content.CallID == issued {
			t.Fatal("the branch's call is stored unqualified and can collide with a sibling's")
		}
	}
}

package airuntime

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/togettoyou/zke/pkg/server/aimodel"
	"github.com/togettoyou/zke/pkg/server/aisession"
)

// failingModel answers with a scripted sequence of failures before its steps.
type failingModel struct {
	mu       sync.Mutex
	failures []error
	steps    []aimodel.Completion
	index    int
	attempts int
	settings aimodel.Settings
}

func (model *failingModel) Get(context.Context) (aimodel.Settings, error) {
	if model.settings.ContextWindowTokens == 0 {
		return aimodel.Settings{
			Enabled: true, ContextWindowTokens: 64_000, MaxOutputTokens: 2_000,
		}, nil
	}
	return model.settings, nil
}

func (model *failingModel) Complete(
	_ context.Context, input aimodel.CompletionInput,
) (aimodel.Completion, aimodel.Budget, error) {
	model.mu.Lock()
	defer model.mu.Unlock()
	if input.System == titlePrompt {
		return aimodel.Completion{Text: "命名结果"}, aimodel.Budget{}, nil
	}
	// Summarizing is its own call beside the turn's steps: it must not consume
	// a scripted step, or a test could not tell a checkpoint from an answer.
	if len(input.Messages) > 0 &&
		input.Messages[len(input.Messages)-1].Text == compactionInstruction {
		return aimodel.Completion{Text: "检查点：早期排查已总结。"}, aimodel.Budget{}, nil
	}
	model.attempts++
	if len(model.failures) > 0 {
		failure := model.failures[0]
		model.failures = model.failures[1:]
		return aimodel.Completion{}, aimodel.Budget{}, failure
	}
	if model.index >= len(model.steps) {
		return answering("没有更多步骤了。"), aimodel.Budget{}, nil
	}
	step := model.steps[model.index]
	model.index++
	return step, aimodel.Budget{}, nil
}

func (model *failingModel) attemptCount() int {
	model.mu.Lock()
	defer model.mu.Unlock()
	return model.attempts
}

// A rate limit from a shared endpoint is the ordinary weather of a deployment
// that does not own its inference service. Ending a turn on one throws away
// everything the turn had already done.
func TestTransientModelFailureIsRetried(t *testing.T) {
	t.Parallel()
	sessions := &memorySessions{session: idleSession(aisession.ApprovalFull)}
	model := &failingModel{
		failures: []error{
			&aimodel.CallError{Kind: aimodel.CallRateLimited, Status: 429},
			&aimodel.CallError{Kind: aimodel.CallServer, Status: 503},
		},
		steps: []aimodel.Completion{answering("集群正常。")},
	}
	runtime := New(context.Background(), sessions, model, allowAuthorizer{},
		activeUsers{true}, Config{
			ModelRetry: RetryConfig{
				MaxRetries: 3, InitialDelay: time.Millisecond, MaxDelay: 2 * time.Millisecond,
			},
		})

	if _, err := runtime.Start(context.Background(), StartInput{
		SessionID: testSessionID, UserID: testUserID, Text: "看一下", Now: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	runtime.Wait()

	if model.attemptCount() != 3 {
		t.Fatalf("model called %d times, want the two failures retried", model.attemptCount())
	}
	if !hasEntry(sessions, aisession.KindConclusion, "集群正常。") {
		t.Fatalf("turn did not conclude: %+v", entryKinds(sessions))
	}
}

// A credential the endpoint refused fails identically on every attempt, and the
// operator has to be told which thing to go and fix rather than being shown a
// generic outage after four pointless retries.
func TestTerminalModelFailureIsNotRetried(t *testing.T) {
	t.Parallel()
	sessions := &memorySessions{session: idleSession(aisession.ApprovalFull)}
	model := &failingModel{failures: []error{
		&aimodel.CallError{Kind: aimodel.CallAuth, Status: 401},
	}}
	runtime := New(context.Background(), sessions, model, allowAuthorizer{},
		activeUsers{true}, Config{
			ModelRetry: RetryConfig{
				MaxRetries: 3, InitialDelay: time.Millisecond, MaxDelay: 2 * time.Millisecond,
			},
		})

	if _, err := runtime.Start(context.Background(), StartInput{
		SessionID: testSessionID, UserID: testUserID, Text: "看一下", Now: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	runtime.Wait()

	if model.attemptCount() != 1 {
		t.Fatalf("model called %d times, want a refused credential to be terminal",
			model.attemptCount())
	}
	if failure := lastFailure(sessions); failure != aisession.FailureModelRejected {
		t.Fatalf("turn failure = %q, want the endpoint's refusal classified", failure)
	}
}

// An exhausted balance is not a passing outage. Reporting it as one sends the
// operator looking at the network instead of at the account.
func TestExhaustedQuotaIsReportedAsItsOwnFailure(t *testing.T) {
	t.Parallel()
	sessions := &memorySessions{session: idleSession(aisession.ApprovalFull)}
	model := &failingModel{failures: []error{
		&aimodel.CallError{Kind: aimodel.CallQuota, Status: 402},
	}}
	runtime := New(context.Background(), sessions, model, allowAuthorizer{},
		activeUsers{true}, Config{})

	if _, err := runtime.Start(context.Background(), StartInput{
		SessionID: testSessionID, UserID: testUserID, Text: "看一下", Now: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	runtime.Wait()

	if failure := lastFailure(sessions); failure != aisession.FailureModelQuota {
		t.Fatalf("turn failure = %q, want the quota reported as itself", failure)
	}
}

// The endpoint's own accounting is the last word on whether a request fits. A
// rejection for size is the one failure a smaller conversation fixes, so the
// turn compacts and sends it again rather than ending.
func TestContextOverflowCompactsAndRetriesTheStep(t *testing.T) {
	t.Parallel()
	sessions := &memorySessions{session: idleSession(aisession.ApprovalFull)}
	sessions.entries = []aisession.Entry{
		{Sequence: 1, Turn: 1, Kind: aisession.KindInput,
			Content: aisession.Content{Text: strings.Repeat("很早以前的问题", 200)}},
		{Sequence: 2, Turn: 1, Kind: aisession.KindModel,
			Content: aisession.Content{Text: strings.Repeat("很早以前的结论", 200)}},
		{Sequence: 3, Turn: 1, Kind: aisession.KindInput,
			Content: aisession.Content{Text: strings.Repeat("最近的问题", 200)}},
	}
	model := &failingModel{
		failures: []error{&aimodel.CallError{Kind: aimodel.CallContextOverflow, Status: 400}},
		steps:    []aimodel.Completion{answering("压缩后仍然能回答。")},
	}
	runtime := New(context.Background(), sessions, model, allowAuthorizer{},
		activeUsers{true}, Config{Compaction: CompactionConfig{MaxOverflowRetries: 1}})

	if _, err := runtime.Start(context.Background(), StartInput{
		SessionID: testSessionID, UserID: testUserID, Text: "继续", Now: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	runtime.Wait()

	compaction := findEntry(sessions, aisession.KindCompaction)
	if compaction == nil {
		t.Fatalf("no compaction was recorded: %+v", entryKinds(sessions))
	}
	if compaction.Content.Compaction.Trigger != aisession.CompactionTriggerOverflow {
		t.Fatalf("compaction trigger = %q, want the endpoint's rejection recorded",
			compaction.Content.Compaction.Trigger)
	}
	if compaction.Content.Compaction.ShadowedTo == 0 {
		t.Fatal("compaction recorded no shadowed span")
	}
	if !hasEntry(sessions, aisession.KindConclusion, "压缩后仍然能回答。") {
		t.Fatalf("turn did not recover: %+v", entryKinds(sessions))
	}
}

// A conversation that cannot be reduced has to end the turn rather than send
// the same oversized request again.
func TestContextOverflowThatCannotBeCompactedEndsTheTurn(t *testing.T) {
	t.Parallel()
	sessions := &memorySessions{session: idleSession(aisession.ApprovalFull)}
	model := &failingModel{failures: []error{
		&aimodel.CallError{Kind: aimodel.CallContextOverflow, Status: 400},
		&aimodel.CallError{Kind: aimodel.CallContextOverflow, Status: 400},
	}}
	runtime := New(context.Background(), sessions, model, allowAuthorizer{},
		activeUsers{true}, Config{Compaction: CompactionConfig{MaxOverflowRetries: 1}})

	if _, err := runtime.Start(context.Background(), StartInput{
		SessionID: testSessionID, UserID: testUserID, Text: "继续", Now: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	runtime.Wait()

	if failure := lastFailure(sessions); failure != aisession.FailureBudgetExceeded {
		t.Fatalf("turn failure = %q, want the budget reported", failure)
	}
}

// countingTools reports the highest number of reads that were ever in flight
// together, so a test can hold the loop to its concurrency bound in both
// directions: reads do run together, and never more than allowed.
type countingTools struct {
	spec     ToolSpec
	inFlight atomic.Int32
	peak     atomic.Int32
	calls    atomic.Int32
}

func (tools *countingTools) Specs() []ToolSpec { return []ToolSpec{tools.spec} }

func (tools *countingTools) Invoke(
	_ context.Context, _ ToolInvocation,
) (ToolResult, error) {
	tools.calls.Add(1)
	current := tools.inFlight.Add(1)
	for {
		peak := tools.peak.Load()
		if current <= peak || tools.peak.CompareAndSwap(peak, current) {
			break
		}
	}
	time.Sleep(5 * time.Millisecond)
	tools.inFlight.Add(-1)
	return ToolResult{Text: "{}"}, nil
}

// One step that asks for four independent reads should take one round trip's
// worth of waiting, not four.
func TestParallelReadsRunTogetherWithinTheBound(t *testing.T) {
	t.Parallel()
	sessions := &memorySessions{session: idleSession(aisession.ApprovalFull)}
	calls := make([]aimodel.ToolCall, 0, 4)
	for index := range 4 {
		calls = append(calls, aimodel.ToolCall{
			ID:   string(rune('a' + index)),
			Name: "cluster_overview", Arguments: `{"index":` + string(rune('0'+index)) + `}`,
		})
	}
	model := &scriptedModel{steps: []aimodel.Completion{
		{ToolCalls: calls}, answering("读完了。"),
	}}
	tools := &countingTools{spec: readOnlyTool()}
	runtime := New(context.Background(), sessions, model, allowAuthorizer{},
		activeUsers{true}, Config{Tools: tools, MaxParallelToolCalls: 2})

	if _, err := runtime.Start(context.Background(), StartInput{
		SessionID: testSessionID, UserID: testUserID, Text: "都看一下", Now: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	runtime.Wait()

	if tools.calls.Load() != 4 {
		t.Fatalf("tool called %d times, want every requested read", tools.calls.Load())
	}
	if peak := tools.peak.Load(); peak < 2 {
		t.Fatalf("peak concurrency = %d, want independent reads to run together", peak)
	}
	if peak := tools.peak.Load(); peak > 2 {
		t.Fatalf("peak concurrency = %d, want the configured bound respected", peak)
	}
}

type mixedMutationTools struct {
	mu       sync.Mutex
	inFlight int
	peak     int
	order    []string
}

func (tools *mixedMutationTools) Specs() []ToolSpec {
	read := readOnlyTool()
	write := mutatingTool()
	return []ToolSpec{read, write}
}

func (tools *mixedMutationTools) Invoke(
	_ context.Context, invocation ToolInvocation,
) (ToolResult, error) {
	tools.mu.Lock()
	tools.inFlight++
	if tools.inFlight > tools.peak {
		tools.peak = tools.inFlight
	}
	tools.order = append(tools.order, invocation.Name)
	tools.mu.Unlock()
	time.Sleep(5 * time.Millisecond)
	tools.mu.Lock()
	tools.inFlight--
	tools.mu.Unlock()
	return ToolResult{Text: "{}"}, nil
}

func TestStepContainingAWriteRunsSeriallyInModelOrder(t *testing.T) {
	t.Parallel()
	sessions := &memorySessions{session: idleSession(aisession.ApprovalFull)}
	model := &scriptedModel{steps: []aimodel.Completion{
		{ToolCalls: []aimodel.ToolCall{
			{ID: "read_before", Name: "cluster_overview", Arguments: `{}`},
			{ID: "write", Name: "scale_workload", Arguments: `{"replicas":3}`},
			{ID: "read_after", Name: "cluster_overview", Arguments: `{"after":true}`},
		}},
		answering("完成。"),
	}}
	tools := &mixedMutationTools{}
	runtime := New(context.Background(), sessions, model, allowAuthorizer{},
		activeUsers{true}, Config{Tools: tools, MaxParallelToolCalls: 3})

	if _, err := runtime.Start(context.Background(), StartInput{
		SessionID: testSessionID, UserID: testUserID, Text: "伸缩并复查", Now: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	runtime.Wait()

	tools.mu.Lock()
	defer tools.mu.Unlock()
	if tools.peak != 1 {
		t.Fatalf("peak concurrency = %d, want a write batch serialized", tools.peak)
	}
	if got := strings.Join(tools.order, ","); got != "cluster_overview,scale_workload,cluster_overview" {
		t.Fatalf("execution order = %s", got)
	}
}

// The trail is read in order by people and by the export. Which read happened
// to finish first is not a fact worth recording, so results are written back in
// the order the model asked for them.
func TestParallelResultsAreRecordedInModelOrder(t *testing.T) {
	t.Parallel()
	sessions := &memorySessions{session: idleSession(aisession.ApprovalFull)}
	model := &scriptedModel{steps: []aimodel.Completion{
		{ToolCalls: []aimodel.ToolCall{
			{ID: "call_1", Name: "cluster_overview", Arguments: `{"index":1}`},
			{ID: "call_2", Name: "cluster_overview", Arguments: `{"index":2}`},
			{ID: "call_3", Name: "cluster_overview", Arguments: `{"index":3}`},
		}},
		answering("读完了。"),
	}}
	runtime := New(context.Background(), sessions, model, allowAuthorizer{},
		activeUsers{true}, Config{Tools: &countingTools{spec: readOnlyTool()}})

	if _, err := runtime.Start(context.Background(), StartInput{
		SessionID: testSessionID, UserID: testUserID, Text: "都看一下", Now: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	runtime.Wait()

	order := make([]string, 0, 3)
	sessions.mu.Lock()
	for _, entry := range sessions.entries {
		if entry.Kind == aisession.KindToolResult {
			order = append(order, entry.Content.CallID)
		}
	}
	sessions.mu.Unlock()
	if strings.Join(order, ",") != "call_1,call_2,call_3" {
		t.Fatalf("tool results recorded as %v, want model order", order)
	}
}

// The endpoint charged us an exact number for the last request. Preferring it
// over the local heuristic is what keeps a long session's estimate from
// drifting: the error is bounded by one step of new content rather than
// accumulating over every step of every turn.
func TestMeasureAnchorsOnReportedUsage(t *testing.T) {
	t.Parallel()
	entries := []aisession.Entry{
		{Sequence: 1, Turn: 1, Kind: aisession.KindInput,
			Content: aisession.Content{Text: strings.Repeat("问题", 100)}},
		{Sequence: 2, Turn: 1, Kind: aisession.KindModel, Content: aisession.Content{
			Text:   "读到了",
			Tokens: &aisession.Tokens{Input: 40_000, Output: 500},
		}},
	}

	pressure := measure(entries, "系统指令", nil)

	if !pressure.Measured {
		t.Fatal("measure() ignored the usage the endpoint reported")
	}
	if pressure.TotalTokens < 40_000 {
		t.Fatalf("measure() total = %d, want the reported usage carried", pressure.TotalTokens)
	}
}

// Usage taken before the newest checkpoint describes a conversation that no
// longer exists, and using it would report a compacted session as still full.
func TestMeasureIgnoresUsageFromBeforeACheckpoint(t *testing.T) {
	t.Parallel()
	entries := []aisession.Entry{
		{Sequence: 1, Turn: 1, Kind: aisession.KindModel, Content: aisession.Content{
			Text: "旧的一步", Tokens: &aisession.Tokens{Input: 40_000, Output: 500},
		}},
		{Sequence: 2, Turn: 1, Kind: aisession.KindCompaction, Content: aisession.Content{
			Text:       "检查点",
			Compaction: &aisession.Compaction{ShadowedFrom: 1, ShadowedTo: 1},
		}},
		{Sequence: 3, Turn: 1, Kind: aisession.KindInput, Content: aisession.Content{Text: "新问题"}},
	}

	pressure := measure(entries, "系统指令", nil)

	if pressure.Measured {
		t.Fatal("measure() reused usage the checkpoint invalidated")
	}
	if pressure.TotalTokens > 1_000 {
		t.Fatalf("measure() total = %d, want a compacted session priced as small",
			pressure.TotalTokens)
	}
}

// A checkpoint shadows a span, not everything before itself. The trail is
// append-only, so a checkpoint is written after the tail it deliberately left
// alone — and "everything before this entry" would swallow it.
func TestSurfaceKeepsTheTailACheckpointDidNotShadow(t *testing.T) {
	t.Parallel()
	entries := []aisession.Entry{
		{Sequence: 1, Turn: 1, Kind: aisession.KindInput, Content: aisession.Content{Text: "被压缩的旧问题"}},
		{Sequence: 2, Turn: 1, Kind: aisession.KindModel, Content: aisession.Content{Text: "保留的最近一步"}},
		{Sequence: 3, Turn: 1, Kind: aisession.KindCompaction, Content: aisession.Content{
			Text:       "检查点正文",
			Compaction: &aisession.Compaction{ShadowedFrom: 1, ShadowedTo: 1},
		}},
	}

	messages, _ := buildMessages(entries)

	joined := ""
	for _, message := range messages {
		joined += message.Text + "\n"
	}
	if strings.Contains(joined, "被压缩的旧问题") {
		t.Fatalf("buildMessages() kept a shadowed entry: %q", joined)
	}
	if !strings.Contains(joined, "检查点正文") || !strings.Contains(joined, "保留的最近一步") {
		t.Fatalf("buildMessages() = %q, want the checkpoint and the retained tail", joined)
	}
}

// A Chinese conversation priced at the Latin density reports a quarter of its
// real size, and the first thing that breaks is compacting before the endpoint
// refuses the request.
func TestTokenEstimateDoesNotUnderpriceChineseText(t *testing.T) {
	t.Parallel()
	chinese := strings.Repeat("集群里的节点状态异常", 100)
	latin := strings.Repeat("cluster node status is abnormal ", 100)

	if estimateTokens(chinese) < len([]rune(chinese))/2 {
		t.Fatalf("estimateTokens() priced Chinese at %d for %d characters",
			estimateTokens(chinese), len([]rune(chinese)))
	}
	if estimateTokens(latin) > len(latin)/2 {
		t.Fatalf("estimateTokens() overpriced Latin text at %d for %d characters",
			estimateTokens(latin), len(latin))
	}
}

// Usage is what the composer's meter reads. It has to be the same measurement
// the loop makes before every request, or the ring would disagree with the
// thing it claims to describe.
func TestUsageReportsTheSameBudgetTheLoopUses(t *testing.T) {
	t.Parallel()
	sessions := &memorySessions{session: idleSession(aisession.ApprovalFull)}
	sessions.entries = []aisession.Entry{
		{Sequence: 1, Turn: 1, Kind: aisession.KindInput,
			Content: aisession.Content{Text: strings.Repeat("问题", 100)}},
	}
	runtime := New(context.Background(), sessions, &failingModel{}, allowAuthorizer{},
		activeUsers{true}, Config{
			Tools:      &scriptedTools{spec: readOnlyTool(), text: "{}"},
			Compaction: CompactionConfig{ThresholdRatio: 0.8, RetainRatio: 0.16},
		})

	usage, err := runtime.Usage(context.Background(), testSessionID, testUserID, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if usage.ContextWindowTokens != 64_000 {
		t.Fatalf("usage window = %d, want the endpoint's", usage.ContextWindowTokens)
	}
	if usage.ThresholdTokens != 51_200 {
		t.Fatalf("usage threshold = %d, want the configured fraction", usage.ThresholdTokens)
	}
	if usage.SystemTokens == 0 || usage.ToolsTokens == 0 || usage.MessageTokens == 0 {
		t.Fatalf("usage breakdown = %+v, want every part priced", usage)
	}
	if usage.UsedTokens < usage.SystemTokens+usage.ToolsTokens+usage.MessageTokens {
		t.Fatalf("usage total = %d, below its own parts", usage.UsedTokens)
	}
}

func entryKinds(sessions *memorySessions) []aisession.Kind {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	kinds := make([]aisession.Kind, 0, len(sessions.entries))
	for _, entry := range sessions.entries {
		kinds = append(kinds, entry.Kind)
	}
	return kinds
}

func findEntry(sessions *memorySessions, kind aisession.Kind) *aisession.Entry {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	for index := range sessions.entries {
		if sessions.entries[index].Kind == kind {
			return &sessions.entries[index]
		}
	}
	return nil
}

func hasEntry(sessions *memorySessions, kind aisession.Kind, text string) bool {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	for _, entry := range sessions.entries {
		if entry.Kind == kind && strings.Contains(entry.Content.Text, text) {
			return true
		}
	}
	return false
}

func lastFailure(sessions *memorySessions) string {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	for index := len(sessions.entries) - 1; index >= 0; index-- {
		if sessions.entries[index].Kind == aisession.KindError {
			return sessions.entries[index].Content.Failure
		}
	}
	return ""
}
